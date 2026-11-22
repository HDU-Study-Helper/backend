package dbbridge

import (
	f "academic/agent/framework"
	c "academic/backend/internal/continuation"
	"context"
	"testing"
	"time"
)

func TestContinuationQuestionsAndKnownWorkAtomic(t *testing.T) {
	db, scope, stage := learnerStageFixture(t)
	store := db.Agents().(c.Store)
	ctx := context.Background()
	newArchive := func(ch, key string) (c.Archive, *c.Question) {
		run := f.NewID()
		must(t, db.Agents().SaveRun(ctx, f.RunRecord{ID: run, AgentID: scope.AgentID, OwnerID: scope.OwnerID, Status: "running", StartedAt: time.Now()}))
		return c.Archive{Source: c.Source{Owner: scope.OwnerID, Channel: ch, Key: key}, Role: "learner", Transcript: f.Transcript{Owner: scope.OwnerID, Agent: scope.AgentID, Run: run}}, &c.Question{ID: f.NewID(), Role: "learner", RunID: run, Channel: ch, Key: key, Text: "Which date?", Interaction: c.Interaction{Question: "Which date?", Type: "TEXT_INPUT"}}
	}
	a, q := newArchive(c.ChA, "changes")
	bound := c.WithCommitBinding(ctx, func(id string, _ []byte) (c.Archive, *c.Question) { a.Path = "REVISE"; a.Target = id; return a, q })
	receipt, e := stage.CommitLearnerStage(bound, initialStage(scope))
	must(t, e)
	saved, e := store.LoadContinuation(ctx, scope.OwnerID, a.Transcript.Run)
	must(t, e)
	if saved.Target != receipt.WorkflowID {
		t.Fatal("archive did not bind committed target")
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM public_actions WHERE owner_id=$1 AND id=$2 AND input->>'mode'='silent'`, scope.OwnerID, "question:"+q.ID).Scan(&count))
	if count != 1 {
		t.Fatal("missing silent notice")
	}
	if _, e = stage.CommitLearnerStage(bound, initialStage(scope)); e == nil {
		t.Fatal("duplicate archive should roll back a separate stage commit")
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1`, scope.OwnerID).Scan(&count))
	if count != 1 {
		t.Fatal("failed question save committed staged work")
	}
	b, qb := newArchive(c.ChB, "thread-1")
	must(t, store.SaveContinuation(ctx, b, qb))
	b2, q2 := newArchive(c.ChB, "thread-2")
	must(t, store.SaveContinuation(ctx, b2, q2))
	must(t, store.CancelContinuations(ctx, b.Source))
	if _, _, e = store.ClaimContinuation(ctx, b.Source, qb.ID, "yes"); e == nil {
		t.Fatal("cancelled task resumed")
	}
	if _, _, e = store.ClaimContinuation(ctx, b2.Source, q.ID, "yes"); e == nil {
		t.Fatal("wrong source answered")
	}
	active, e := store.PendingContinuations(ctx, a.Source)
	must(t, e)
	if len(active) != 1 {
		t.Fatal("Chat cancellation touched ChA")
	}
	active, e = store.PendingContinuations(ctx, b2.Source)
	must(t, e)
	if len(active) != 1 {
		t.Fatal("Chat cancellation touched unrelated thread")
	}
	if _, done, e := store.ClaimContinuation(ctx, a.Source, q.ID, "yes"); e != nil || done != nil {
		t.Fatal(e)
	}
	if _, _, e = store.ClaimContinuation(ctx, a.Source, q.ID, "yes"); e == nil {
		t.Fatal("duplicate claim executed")
	}
	must(t, store.FinishContinuation(ctx, a.Source, q.ID, f.RunResult{RunID: "resumed", Output: "answer"}))
	if _, done, e := store.ClaimContinuation(ctx, a.Source, q.ID, "yes"); e != nil || done == nil || done.RunID != "resumed" {
		t.Fatal("lost answer receipt", e)
	}
	if _, _, e = store.ClaimContinuation(ctx, a.Source, q.ID, "different"); e == nil {
		t.Fatal("accepted different duplicate answer")
	}
}
