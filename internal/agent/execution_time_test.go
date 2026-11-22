package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	f "academic/agent/framework"
)

func applicationReferenceTime(t *testing.T, request f.ModelRequest) map[string]string {
	t.Helper()
	for _, line := range strings.Split(request.Messages[0].Content, "\n") {
		if strings.HasPrefix(line, `{"current_time_utc":`) {
			var fields map[string]string
			if err := json.Unmarshal([]byte(line), &fields); err != nil {
				t.Fatal(err)
			}
			return fields
		}
	}
	t.Fatal("trusted time missing from system message")
	return nil
}

func verifyApplicationReferenceTime(t *testing.T, fields map[string]string, record f.RunRecord) {
	t.Helper()
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if fields["time_zone"] != "Asia/Shanghai" || fields["current_time_utc"] != record.StartedAt.UTC().Format(time.RFC3339Nano) || fields["current_time_local"] != record.StartedAt.In(zone).Format(time.RFC3339Nano) || fields["local_date"] != record.StartedAt.In(zone).Format("2006-01-02") {
		t.Fatal("application did not use this Run's start in the product zone", fields, record.StartedAt)
	}
}

func TestServiceExecutionTimeForParentAndDispatchedChild(t *testing.T) {
	parent, child := testAgent("parent", "owner"), testAgent("child", "owner")
	repo := &repoStub{agents: map[string]f.AgentInstance{parent.ID: parent, child.ID: child}, runs: map[string]f.RunRecord{}}
	seen := map[string]map[string]string{}
	service, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{
		parent.ID: fnModel(func(_ context.Context, request f.ModelRequest) (f.ModelResponse, error) {
			seen[parent.ID] = applicationReferenceTime(t, request)
			if request.Messages[len(request.Messages)-1].Role == "tool" {
				return done(), nil
			}
			return toolCall(child.ID), nil
		}),
		child.ID: fnModel(func(_ context.Context, request f.ModelRequest) (f.ModelResponse, error) {
			seen[child.ID] = applicationReferenceTime(t, request)
			return done(), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(context.Background(), "owner", f.RunRequest{AgentID: parent.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{"time_zone":"UTC","local_date":"1900-01-01","current_time_utc":"1900-01-01T00:00:00Z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.runs) != 2 {
		t.Fatal("dispatch did not produce two actual runs", len(repo.runs))
	}
	var starts []time.Time
	for _, record := range repo.runs {
		verifyApplicationReferenceTime(t, seen[record.AgentID], record)
		starts = append(starts, record.StartedAt)
	}
	if starts[0].Equal(starts[1]) {
		t.Fatal("child reused the parent's clock rather than its own recorded start")
	}
}

func TestServiceExecutionTimeReachesTemporaryLearnerRuntime(t *testing.T) {
	repo := stagingFixture()
	var seen map[string]string
	service, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, request f.ModelRequest) (f.ModelResponse, error) {
		seen = applicationReferenceTime(t, request)
		state := stagingState(t, request)
		out := map[string]any{"protocol": "learner_staging_v2", "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": "", "base_version": nil, "entity": nil, "status": "NEEDS_INFO", "operation": "NONE", "completion": nil, "summary": "course identity unknown", "reason": "fixture supplies no course", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{"Which course?"}, "limitations": []any{}}
		raw, _ := json.Marshal(out)
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(raw)}, FinishReason: "stop"}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(WithLearnerStage(context.Background(), "owner", "INITIALIZE", ""), "owner", f.RunRequest{AgentID: "learner", Trigger: f.SystemEvent, Input: json.RawMessage(`{"time_zone":"UTC","local_date":"1900-01-01"}`)})
	if err != nil {
		t.Fatal(err)
	}
	verifyApplicationReferenceTime(t, seen, repo.runs[result.RunID])
	if repo.commits != 0 {
		t.Fatal("clock injection changed a blocked Learner into a commit")
	}
}

func TestServiceExecutionTimeRejectsForeignHostZone(t *testing.T) {
	repo := &repoStub{agents: map[string]f.AgentInstance{}, runs: map[string]f.RunRecord{}}
	service, err := New(Dependencies{Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := f.WithExecutionTimeZone(context.Background(), "other", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Run(ctx, "owner", f.RunRequest{AgentID: "any", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	errCode(t, err, "PERMISSION_DENIED")
	if len(repo.runs) != 0 {
		t.Fatal("foreign time context created an execution")
	}
}
