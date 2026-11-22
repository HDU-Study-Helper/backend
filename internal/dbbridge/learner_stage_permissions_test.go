package dbbridge

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
)

func TestLearnerStagePostgresPreservesUnchangedKnowledgeAndRevocation(t *testing.T) {
	db, scope, store := learnerStageFixture(t)
	ctx := context.Background()
	create := initialStage(scope)
	create.Desired.Memories = append(create.Desired.Memories, f.Memory{ID: "another", OwnerID: scope.OwnerID, WorkflowID: "staged-workflow", Visibility: "PRI", Title: "Unchanged", Content: "stable evidence", Level: "L1", MemoryType: "BASE", Status: "active", SourceRefs: json.RawMessage(`["record-2"]`)})
	r, e := store.CommitLearnerStage(ctx, create)
	must(t, e)
	base, e := store.LearnerSnapshot(ctx, scope, r.WorkflowID)
	must(t, e)
	update := stage.Commit{SessionID: f.NewID(), Scope: scope, Path: "REVISE", Operation: "UPDATE", Revision: 2, Base: &base, BaselineHash: stage.Hash(base), Desired: stage.Clone(base)}
	update.Desired.Memories[0].Content = "specific corrected fact"
	learner, e := db.Agents().GetAgent(ctx, scope)
	must(t, e)
	originalTools := append([]string(nil), learner.EnabledTools...)
	tools := []string{}
	for _, v := range learner.EnabledTools {
		if v != "memory_update" {
			tools = append(tools, v)
		}
	}
	learner.EnabledTools = tools
	_, e = db.Agents().PutAgent(ctx, learner)
	must(t, e)
	_, e = store.CommitLearnerStage(ctx, update)
	code(t, e, "PERMISSION_DENIED")
	learner.EnabledTools = originalTools
	_, e = db.Agents().PutAgent(ctx, learner)
	must(t, e)
	_, e = store.CommitLearnerStage(ctx, update)
	must(t, e)
	after, e := store.LearnerSnapshot(ctx, scope, r.WorkflowID)
	must(t, e)
	if after.Workflow.Version != base.Workflow.Version {
		t.Fatal("memory-only update bumped Workflow")
	}
	if !after.Memories[1].UpdatedAt.Equal(base.Memories[1].UpdatedAt) {
		t.Fatal("unmodified memory timestamp changed")
	}
}
