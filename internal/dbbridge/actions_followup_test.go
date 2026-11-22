package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/actions"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func actionTestActor(t *testing.T, db *DB, name string, tools, perms []string) (model.ActionScope, f.AgentInstance) {
	t.Helper()
	ctx := context.Background()
	u := fixtureUser(t, db, name, model.RoleUser)
	a, e := (f.Registry{Repository: db.Agents()}).Create(ctx, u.ID, definition(name, "mock", tools, perms))
	must(t, e)
	s := model.ActionScope{OwnerID: u.ID, AgentID: a.ID, RunID: newID()}
	must(t, db.Agents().SaveRun(ctx, f.RunRecord{ID: s.RunID, OwnerID: u.ID, AgentID: a.ID, Status: "running", StartedAt: time.Now()}))
	return s, a
}
func TestActionChAAnswerProof(t *testing.T) { testActionAnswerProof(t, c.ChA) }
func TestActionChBAnswerProof(t *testing.T) { testActionAnswerProof(t, c.ChB) }
func testActionAnswerProof(t *testing.T, channel string) {
	ctx := context.Background()
	db := testDB(t)
	s, _ := actionTestActor(t, db, "proof-owner", nil, nil)
	other, _ := actionTestActor(t, db, "proof-other", nil, nil)
	src := c.Source{Owner: s.OwnerID, Channel: channel, Key: "source"}
	q := c.Question{ID: "pending-question", Role: "worker", RunID: s.RunID, Channel: src.Channel, Key: src.Key, Text: "model-generated question", Interaction: c.Interaction{Question: "model-generated question", Type: "TEXT_INPUT"}}
	store := db.Agents().(c.Store)
	must(t, store.SaveContinuation(ctx, c.Archive{Source: src, Role: q.Role, Transcript: f.Transcript{Owner: s.OwnerID, Run: s.RunID, Agent: s.AgentID}}, &q))
	r := db.Actions()
	in := model.ActionInput{Title: "Math", Body: "Study", Reason: "Owner asked", Key: "remind", Mode: "active", Evidence: []string{"real event"}, IntentQuote: q.Text, IntentTurn: q.ID}
	denied := func(s model.ActionScope) {
		t.Helper()
		_, e := r.PutAction(ctx, s, "notice", in)
		if !errors.Is(e, model.ErrInvalidArgument) {
			t.Fatal("unverified intent", e)
		}
	}
	denied(s)
	_, _, e := store.ClaimContinuation(ctx, src, q.ID, "Please remind me on Tuesdays about math.")
	must(t, e)
	denied(s)
	in.IntentQuote = "remind me on Tuesdays"
	denied(other)
	_, e = r.PutAction(ctx, s, "notice", in)
	must(t, e)
	_, e = db.pool.Exec(ctx, `INSERT INTO memories(memory_id,owner_user_id,visibility,memory_type,is_personal,level,title,content,source_refs) VALUES('pref',$1,'PUB','BASE',true,'L1','tuesdays','math','["chat_turn:pending-question"]')`, s.OwnerID)
	must(t, e)
	in.PreferenceID = "pref"
	in.Key = "weekly"
	denied(s)
	_, e = db.pool.Exec(ctx, `UPDATE memories SET source_refs='["question_answer:pending-question"]' WHERE owner_user_id=$1 AND memory_id='pref'`, s.OwnerID)
	must(t, e)
	_, e = r.PutAction(ctx, s, "notice", in)
	must(t, e)
	in.Key = "after-change"
	_, e = db.pool.Exec(ctx, `UPDATE agent_continuation_questions SET answer_hash='wrong' WHERE owner_user_id=$1 AND question_id=$2`, s.OwnerID, q.ID)
	must(t, e)
	denied(s)
}
func TestActionScheduledTodoReceiptAndRevocation(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s, a := actionTestActor(t, db, "scheduled-actor", []string{"planned_job_create", "public_todo_upsert", "public_todo_complete"}, []string{"job.write", "todo.write"})
	r := db.Actions()
	now := time.Now()
	job := model.ActionInput{Key: "j1", Kind: "ToolCall", Tool: "public_todo_upsert", Evidence: []string{"msg:123"}, Due: &now, Args: json.RawMessage(`{"todo_key":"math-work","title":"Read"}`)}
	x := actions.Executor{Repo: r, Agents: db.Agents()}
	j, e := r.PutAction(ctx, s, "job", job)
	must(t, e)
	_, e = x.RunOne(ctx)
	must(t, e)
	result, e := r.GetAction(ctx, s.OwnerID, "job", j.ID)
	must(t, e)
	var rec struct{ ID, Status string }
	must(t, json.Unmarshal(result.Result, &rec))
	item, e := r.GetAction(ctx, s.OwnerID, "todo", rec.ID)
	must(t, e)
	if item.Input.Key != "internal:job:"+j.ID || result.ResultRef != "todo:"+rec.ID || rec.Status != "open" {
		t.Fatal("missing real receipt", result, item)
	}
	job.Key = "j2"
	job.Tool = "public_todo_complete"
	job.Args, _ = json.Marshal(map[string]string{"id": item.ID})
	j, e = r.PutAction(ctx, s, "job", job)
	must(t, e)
	a.Permissions = []string{"job.write"}
	_, e = db.Agents().PutAgent(ctx, a)
	must(t, e)
	_, e = x.RunOne(ctx)
	if e == nil {
		t.Fatal("revoked target executed")
	}
	still, e := r.GetAction(ctx, s.OwnerID, "todo", item.ID)
	must(t, e)
	if still.Status != "open" {
		t.Fatal(still)
	}
	a.Permissions = append(a.Permissions, "todo.write")
	_, e = db.Agents().PutAgent(ctx, a)
	must(t, e)
	job.Key = "j3"
	_, e = r.PutAction(ctx, s, "job", job)
	must(t, e)
	_, e = x.RunOne(ctx)
	must(t, e)
	done, e := r.GetAction(ctx, s.OwnerID, "todo", item.ID)
	must(t, e)
	if done.Status != "complete" {
		t.Fatal(done)
	}
	job.Key = "bad"
	job.Tool = "public_todo_upsert"
	job.Args = json.RawMessage(`{"todo_key":"key","title":"bad","owner_id":"victim"}`)
	_, e = r.PutAction(ctx, s, "job", job)
	if !errors.Is(e, model.ErrInvalidArgument) {
		t.Fatal("nested actor injection", e)
	}
}
