package dbbridge

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
	"academic/backend/internal/model"
)

func learnerStageFixture(t *testing.T) (*DB, f.Scope, stage.Store) {
	t.Helper()
	db := testDB(t)
	u := fixtureUser(t, db, "staging-owner", model.RoleUser)
	d := definition("Learner", "mock", []string{"workflow_create", "workflow_update", "workflow_delete", "memory_create", "memory_update", "memory_delete"}, []string{"workflow.read", "workflow.write", "workflow.delete", "memory.read", "memory.write"})
	d.Type = "learner"
	a, e := (f.Registry{Repository: db.Agents()}).Create(context.Background(), u.ID, d)
	must(t, e)
	return db, f.Scope{OwnerID: u.ID, AgentID: a.ID}, db.Agents().(stage.Store)
}
func initialStage(scope f.Scope) stage.Commit {
	return stage.Commit{SessionID: f.NewID(), Scope: scope, Path: "INITIALIZE", Operation: "CREATE", Revision: 3, Desired: stage.Snapshot{Workflow: f.Workflow{ID: "staged-workflow", Name: "Calculus tracking", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"specific-calculus","name":"Calculus"},"goal":"track coursework"}`)}, Memories: []f.Memory{{ID: "staged-memory", OwnerID: scope.OwnerID, WorkflowID: "staged-workflow", Visibility: "PRI", Title: "Course evidence", Content: "teacher and course details", Level: "L1", MemoryType: "BASE", Status: "active", SourceRefs: json.RawMessage(`["record-1"]`)}}, Relations: []f.MemoryRelation{}}}
}
func TestLearnerStagePostgresAtomicIdempotentAndConflicts(t *testing.T) {
	db, scope, store := learnerStageFixture(t)
	ctx := context.Background()
	c := initialStage(scope)
	receipt, e := store.CommitLearnerStage(ctx, c)
	must(t, e)
	if !receipt.Verified || receipt.WorkflowID == "staged-workflow" || len(receipt.MemoryIDs) != 1 {
		t.Fatal(receipt)
	}
	again, e := store.CommitLearnerStage(ctx, c)
	must(t, e)
	if again.WorkflowID != receipt.WorkflowID {
		t.Fatal("commit replay duplicated target")
	}
	changed := c
	changed.Revision++
	_, e = store.CommitLearnerStage(ctx, changed)
	code(t, e, "CONFLICT")
	snap, e := store.LearnerSnapshot(ctx, scope, receipt.WorkflowID)
	must(t, e)
	update := stage.Commit{SessionID: f.NewID(), Scope: scope, Path: "REVISE", Operation: "UPDATE", Revision: 2, Base: &snap, BaselineHash: stage.Hash(snap), Desired: stage.Clone(snap)}
	update.Desired.Memories[0].Content = "staged revision"
	concurrent := snap.Memories[0]
	concurrent.Content = "concurrent change"
	_, e = db.Agents().SaveMemory(ctx, f.Scope{OwnerID: scope.OwnerID, AgentID: scope.AgentID, WorkflowID: receipt.WorkflowID}, concurrent, false)
	must(t, e)
	_, e = store.CommitLearnerStage(ctx, update)
	code(t, e, "CONFLICT")
	current, e := store.LearnerSnapshot(ctx, scope, receipt.WorkflowID)
	must(t, e)
	if current.Memories[0].Content != "concurrent change" {
		t.Fatal("lost concurrent memory change")
	}
	update.SessionID = f.NewID()
	update.Base = &current
	update.BaselineHash = stage.Hash(current)
	update.Desired = stage.Clone(current)
	update.Desired.Memories[0].Content = "accepted revision"
	r2, e := store.CommitLearnerStage(ctx, update)
	must(t, e)
	if r2.WorkflowVersion != 1 {
		t.Fatal(r2)
	}
	_, e = store.LearnerSnapshot(ctx, f.Scope{OwnerID: "foreign", AgentID: scope.AgentID}, receipt.WorkflowID)
	if e == nil {
		t.Fatal("foreign snapshot allowed")
	}
}
func TestLearnerStagePostgresRollbackAndNoChange(t *testing.T) {
	db, scope, store := learnerStageFixture(t)
	ctx := context.Background()
	c := initialStage(scope)
	c.Desired.Memories[0].WorkflowID = "foreign"
	_, e := store.CommitLearnerStage(ctx, c)
	code(t, e, "PERMISSION_DENIED")
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1`, scope.OwnerID).Scan(&count))
	if count != 0 {
		t.Fatal("failed commit leaked Workflow")
	}
	c = initialStage(scope)
	r, e := store.CommitLearnerStage(ctx, c)
	must(t, e)
	snap, e := store.LearnerSnapshot(ctx, scope, r.WorkflowID)
	must(t, e)
	no := stage.Commit{SessionID: f.NewID(), Scope: scope, Path: "REVISE", Operation: "NONE", Revision: 1, Base: &snap, BaselineHash: stage.Hash(snap), Desired: stage.Clone(snap)}
	done, e := store.CommitLearnerStage(ctx, no)
	must(t, e)
	if done.Status != "NO_CHANGE" || done.WorkflowVersion != 1 {
		t.Fatal(done)
	}
	no.SessionID = f.NewID()
	no.Desired.Memories[0].Content = "hidden change"
	_, e = store.CommitLearnerStage(ctx, no)
	code(t, e, "INVALID_ARGUMENT")
	archive := no
	archive.SessionID = f.NewID()
	archive.Operation = "ARCHIVE"
	archive.Desired = stage.Clone(snap)
	archive.Desired.Workflow.Status = "archived"
	_, e = store.CommitLearnerStage(ctx, archive)
	must(t, e)
	archived, e := store.LearnerSnapshot(ctx, scope, r.WorkflowID)
	must(t, e)
	if archived.Workflow.Status != "archived" {
		t.Fatal("archive not committed")
	}
	del := stage.Commit{SessionID: f.NewID(), Scope: scope, Path: "REVISE", Operation: "DELETE", Revision: 1, Base: &archived, BaselineHash: stage.Hash(archived), Desired: stage.Clone(archived)}
	_, e = store.CommitLearnerStage(ctx, del)
	must(t, e)
	_, e = db.Agents().GetWorkflow(ctx, scope.OwnerID, r.WorkflowID)
	code(t, e, "RESOURCE_NOT_FOUND")
	replay, e := store.CommitLearnerStage(ctx, del)
	must(t, e)
	if replay.Status != "DELETED" {
		t.Fatal(replay)
	}
}
