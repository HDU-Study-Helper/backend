package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"academic/backend/internal/runcontrol"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type firstMock func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (m firstMock) Complete(c context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	return m(c, r)
}
func TestFirstRunMinimalLoop(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "first-student", model.RoleAdmin).ID
	conn := activeAcademicConnection(t, db, owner, "chaoxing")
	jwxt := activeAcademicConnection(t, db, owner, "jwxt")
	now := time.Now().UTC()
	local := now.In(academicLocation)
	year, term := local.Year(), 1
	if local.Month() < 8 {
		year--
		if local.Month() >= 2 {
			term = 2
		}
	}
	for i, title := range []string{"Calculus", "Physics", "AlreadyDone"} {
		insertAcademicFixture(t, db, owner, jwxt, academicFixture(int64(i+1), "jwxt", "course", title, "", title, map[string]any{"academic_year": year, "term": term}))
	}
	for i, title := range []string{"《Ｃａｌｃｕｌｕｓ（第６期）》YesterdayExam", "Calculus EightDaysAgo", "Calculus NoPublishTime", "Physics RecentRequirement"} {
		fields := map[string]any{}
		if i != 2 {
			days := 1
			if i == 1 {
				days = 8
			}
			fields["sendTime"] = now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		}
		body := academicFixture(int64(i+4), "chaoxing", "notice", title, "", title, fields)
		insertAcademicFixture(t, db, owner, conn, body)
	}
	must(t, db.transaction(ctx, func(tx pgx.Tx) error { return syncAcademicConnection(ctx, tx, owner, conn) }))
	repo := db.Control()
	first := repo.(model.FirstRunRepository)
	reg := f.Registry{Repository: db.Agents()}
	d := definition("learner", "mock", []string{"data_query", "data_search", "memory_create", "workflow_create"}, []string{"data.read", "memory.write", "workflow.write", "workflow.read"})
	d.Type = "learner"
	d.Limits.Unlimited = true
	d.Limits.MaxConcurrent = 2
	_, err := reg.Create(ctx, owner, d)
	must(t, err)
	d = definition("trigger", "mock", nil, nil)
	d.Type = "trigger"
	_, err = reg.Create(ctx, owner, d)
	must(t, err)
	change := func(id string) {
		t.Helper()
		must(t, db.transaction(ctx, func(tx pgx.Tx) error {
			return captureControl(ctx, tx, owner, "ucampus", []model.ControlChange{{RecordID: id, Operation: "created", Resource: "notice", OccurredAt: now, After: json.RawMessage(`{"title":"delta"}`)}})
		}))
		must(t, repo.Seal(ctx))
	}
	change("before")
	_, err = repo.Claim(ctx, "probe")
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatal("auto started before click", err)
	}
	s, err := first.StartFirst(ctx, owner)
	must(t, err)
	again, err := first.StartFirst(ctx, owner)
	must(t, err)
	if again != s {
		t.Fatal("duplicate first run")
	}
	change("during")
	packet, err := first.FirstInput(ctx, owner, s.Batch)
	must(t, err)
	batch, err := repo.GetBatch(ctx, s.Batch)
	must(t, err)
	if len(packet.Targets) != 3 {
		t.Fatalf("expected three courses, got %d", len(packet.Targets))
	}
	for _, c := range packet.Targets {
		if c.Course.Title != "AlreadyDone" {
			continue
		}
		h := sha256.Sum256([]byte(batch.ID + "/" + c.Course.Ref))
		must(t, repo.(model.ControlItemRepository).SaveItem(ctx, model.ControlItem{ID: "item_" + hex.EncodeToString(h[:16]), OwnerID: owner, BatchID: batch.ID, RoundID: batch.RoundID, Title: c.Course.Title, Status: "COMPLETED"}))
	}
	var oldID string
	must(t, db.pool.QueryRow(ctx, `SELECT id FROM academic_items WHERE title='Calculus EightDaysAgo'`).Scan(&oldID))
	exact := f.Query{Dataset: "academic_items", Fields: []string{"id"}, Filters: []f.Filter{{Field: "id", Operator: "eq", Value: oldID}}, Limit: 1}
	scope := f.Scope{OwnerID: owner}
	ordinary, err := db.AgentData().Query(ctx, scope, exact)
	must(t, err)
	bounded, err := db.AgentData().Query(model.WithFirstReadWindow(ctx, owner, packet.AsOf), scope, exact)
	must(t, err)
	if len(ordinary.Rows) != 1 || len(bounded.Rows) != 0 {
		t.Fatal("exact-ID reads did not preserve the seven-day boundary")
	}
	var calls, started atomic.Int32
	enterTogether := make(chan struct{})
	mock := firstMock(func(ctx context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		n := calls.Add(1)
		reply := func(v string) f.ModelResponse {
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: v}, FinishReason: "stop"}
		}
		if n == 23 {
			return reply(`{"decision":"IGNORE","reason":"fixture","relevant_indices":[]}`), nil
		}
		if n > 23 {
			return f.ModelResponse{}, errors.New("unexpected repeated run")
		}
		call := func(name string, args any) f.ModelResponse {
			return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: fmt.Sprint(n), Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(academicJSON(args))}}}}, FinishReason: "tool_calls"}
		}
		if !strings.HasPrefix(q.Messages[0].Content, "首次运行：") || !strings.Contains(q.Messages[0].Content, "Handle the supplied test task.") {
			return f.ModelResponse{}, errors.New("original system prompt replaced")
		}
		var inp struct {
			Input struct {
				Entity map[string]string
				ItemID string `json:"item_id"`
			}
		}
		for _, m := range q.Messages {
			if m.Role == "user" && strings.HasPrefix(m.Content, "{") {
				_ = json.Unmarshal([]byte(m.Content), &inp)
				break
			}
		}
		name := inp.Input.Entity["name"]
		var want, foreign string
		switch name {
		case "Calculus":
			want, foreign = "YesterdayExam", "Physics"
		case "Physics":
			want, foreign = "RecentRequirement", "Calculus"
		default:
			return f.ModelResponse{}, fmt.Errorf("unexpected course (already completed or unknown): %s", name)
		}
		var recordID string
		toolBodies := ""
		for _, m := range q.Messages {
			if m.Role == "tool" {
				toolBodies += m.Content
				var response struct {
					Data f.KeywordSearchResult
				}
				_ = json.Unmarshal([]byte(m.Content), &response)
				if len(response.Data.Hits) > 0 {
					recordID, _ = response.Data.Hits[0].Row["id"].(string)
				}
			}
		}
		if strings.Contains(toolBodies, "EightDaysAgo") || strings.Contains(toolBodies, "NoPublishTime") {
			return f.ModelResponse{}, errors.New("evidence escaped snapshot boundary")
		}
		step := 1
		for _, m := range q.Messages {
			if m.Role == "assistant" {
				step++
			}
		}
		if step == 1 {
			if started.Add(1) == 2 {
				close(enterTogether)
			}
			select {
			case <-enterTogether:
			case <-ctx.Done():
				return f.ModelResponse{}, errors.New("course runs did not overlap")
			}
		}
		switch step {
		case 1:
			// Look for a different course's evidence to prove that the backend
			// does not prefilter the archive by the assigned course.
			keyword := "RecentRequirement"
			if foreign == "Calculus" {
				keyword = "YesterdayExam"
			}
			return call("data_search", map[string]any{"keywords": []string{keyword}, "datasets": []string{"platform_records"}}), nil
		case 2:
			if !strings.Contains(strings.ToLower(toolBodies), strings.ToLower(foreign)) && !(foreign == "Calculus" && strings.Contains(toolBodies, "YesterdayExam")) {
				return f.ModelResponse{}, errors.New("seven-day cross-course search was prefiltered")
			}
			return call("data_search", map[string]any{"keywords": []string{want, "EightDaysAgo", "NoPublishTime"}, "datasets": []string{"academic_items"}}), nil
		case 3:
			if recordID == "" || !strings.Contains(toolBodies, want) {
				return f.ModelResponse{}, errors.New("missing recent notice")
			}
			return call("data_query", map[string]any{"dataset": "academic_items", "fields": []string{"id", "title", "text", "published_at"}, "filters": []any{map[string]string{"field": "id", "operator": "eq", "value": recordID}}}), nil
		case 4:
			return call("workflow_create", map[string]any{"name": name, "status": "active", "definition": map[string]any{"entity": inp.Input.Entity, "goal": "track current course requirements"}}), nil
		case 6:
			return call("memory_create", map[string]any{"title": name + " assessment notice", "content": name + " announced " + want, "source_refs": []any{map[string]string{"dataset": "academic_items", "id": recordID}}}), nil
		case 7, 8, 9, 10:
			return call("learner_stage_state", map[string]any{}), nil
		case 5, 11:
			var state map[string]any
			last := q.Messages[len(q.Messages)-1].Content
			line := strings.SplitN(last, "data):\n", 2)
			if len(line) != 2 || json.Unmarshal([]byte(strings.SplitN(line[1], "\n", 2)[0]), &state) != nil {
				return f.ModelResponse{}, errors.New("missing trusted staging state")
			}
			if step == 5 {
				return call("learner_stage_select", map[string]any{"workflow_id": state["workflow_id"]}), nil
			}
			if strings.Contains(toolBodies, `"code":"PERMISSION_DENIED"`) {
				return f.ModelResponse{}, errors.New("session refused the Workflow it just created")
			}
			return reply(string(academicJSON(map[string]any{"protocol": state["protocol"], "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": inp.Input.ItemID, "parent_item_id": nil, "workflow_id": state["workflow_id"], "base_version": state["base_version"], "entity": inp.Input.Entity, "status": "READY", "operation": "CREATE", "completion": map[string]any{"marker": "READY_TO_COMMIT", "session_id": state["session_id"], "staging_revision": state["staging_revision"]}, "summary": "saved " + want, "reason": "read relevant notice", "evidence_refs": []string{recordID}, "changes": []any{}, "questions": []any{}, "limitations": []any{}}))), nil
		}
		return f.ModelResponse{}, errors.New("unexpected request stage")
	})
	svc, err := agent.New(agent.Dependencies{Repository: db.Agents(), Providers: map[string]f.ModelProvider{"mock": mock}, Backend: agent.BackendServices{Data: db.AgentData()}})
	must(t, err)
	ctl := runcontrol.New(repo, svc, db.AgentData())
	loop, stop := context.WithTimeout(ctx, 12*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); _ = ctl.Run(loop) }()
	defer func() { stop(); <-done }()
	for loop.Err() == nil {
		must(t, repo.Seal(ctx))
		var finished int
		must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_batches WHERE status='ignored' AND after_first_click`).Scan(&finished))
		if finished == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	v, err := first.FirstStatus(ctx, owner)
	must(t, err)
	var stored, pending, artifacts int
	must(t, db.pool.QueryRow(ctx, `select (select count(*) from memories where owner_workflow_id is not null),(select count(*) from control_batches where status='pending'),(select count(*) from control_artifacts where type='MEMORY')`).Scan(&stored, &pending, &artifacts))
	if loop.Err() != nil || v.State != "succeeded" || calls.Load() != 23 || stored != 2 || artifacts != 2 || pending != 1 {
		t.Fatalf("status=%+v calls=%d memories=%d pending=%d artifacts=%d", v, calls.Load(), stored, pending, artifacts)
	}
}
