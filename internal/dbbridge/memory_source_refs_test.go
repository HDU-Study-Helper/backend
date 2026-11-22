package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func memoryEvidenceFixture(t *testing.T, db *DB, owner, connection, external string) string {
	t.Helper()
	ctx := context.Background()
	_, err := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,$2,'chaoxing','active')`, owner, connection)
	must(t, err)
	var id string
	must(t, db.pool.QueryRow(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,$2,'chaoxing','notice',$3,'','fixture','{"title":"Course notice"}',now()) RETURNING id::text`, owner, connection, external).Scan(&id))
	return id
}
func memoryEvidenceRef(id, connection, external string) json.RawMessage {
	raw, _ := json.Marshal([]any{map[string]string{"dataset": "platform_records", "id": id, "connection_id": connection, "external_id": external}})
	return raw
}

func TestMemorySourceRefsPostgresValidMismatchAndLegacy(t *testing.T) {
	db, scope, _ := learnerStageFixture(t)
	ctx := context.Background()
	id := memoryEvidenceFixture(t, db, scope.OwnerID, "evidence", "4143")
	memory := f.Memory{Title: "notice evidence", Content: "verified fact", SourceRefs: memoryEvidenceRef(id, "evidence", "4143")}
	created, err := db.Agents().SaveMemory(ctx, scope, memory, true)
	must(t, err)
	wrong := created
	wrong.Content = "must not replace the existing fact"
	wrong.SourceRefs = memoryEvidenceRef(id, "evidence", "4142")
	_, err = db.Agents().SaveMemory(ctx, scope, wrong, false)
	code(t, err, "INVALID_ARGUMENT")
	if !strings.Contains(err.Error(), `external_id="4142"`) || !strings.Contains(err.Error(), `authorized external_id="4143"`) {
		t.Fatal("mismatch did not provide the actual authorized correction", err)
	}
	unchanged, err := db.Agents().GetMemory(ctx, scope, created.ID)
	must(t, err)
	if unchanged.Content != created.Content || string(unchanged.SourceRefs) != string(created.SourceRefs) || !unchanged.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatal("rejected reference modified persisted memory", unchanged)
	}
	wrong.ID = ""
	_, err = db.Agents().SaveMemory(ctx, scope, wrong, true)
	code(t, err, "INVALID_ARGUMENT")
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE owner_user_id=$1`, scope.OwnerID).Scan(&count))
	if count != 1 {
		t.Fatal("invalid create persisted a memory", count)
	}
	// Legacy strings, incomplete objects, foreign datasets and other preexisting
	// array elements remain compatible; no new required reference shape appears.
	legacy := f.Memory{Title: "legacy", Content: "old shape", SourceRefs: json.RawMessage(`["record-1",{"dataset":"platform_records","id":"missing"},{"dataset":"platform_records","id":"missing","connection_id":"evidence"},{"dataset":"academic_items","id":"missing","connection_id":"evidence","external_id":"4142"},null,123]`)}
	_, err = db.Agents().SaveMemory(ctx, scope, legacy, true)
	must(t, err)
}

func TestMemorySourceRefsPostgresDatasetRemovalCannotBypassKnownMapping(t *testing.T) {
	db, scope, _ := learnerStageFixture(t)
	ctx := context.Background()
	id := memoryEvidenceFixture(t, db, scope.OwnerID, "evidence", "4143")
	created, err := db.Agents().SaveMemory(ctx, scope, f.Memory{Title: "explicit source", Content: "original", SourceRefs: memoryEvidenceRef(id, "evidence", "4143")}, true)
	must(t, err)
	withoutDataset := func(id, connection, external string) json.RawMessage {
		raw, _ := json.Marshal([]any{map[string]string{"id": id, "connection_id": connection, "external_id": external}})
		return raw
	}
	attempt := created
	attempt.Content = "wrong mapping must not save"
	attempt.SourceRefs = withoutDataset(id, "evidence", "4142")
	_, err = db.Agents().SaveMemory(ctx, scope, attempt, false)
	code(t, err, "INVALID_ARGUMENT")
	if !strings.Contains(err.Error(), `authorized external_id="4143"`) {
		t.Fatal("dataset removal hid the authorized correction", err)
	}
	unchanged, err := db.Agents().GetMemory(ctx, scope, created.ID)
	must(t, err)
	if unchanged.Content != created.Content || string(unchanged.SourceRefs) != string(created.SourceRefs) {
		t.Fatal("dataset-free incorrect reference saved", unchanged)
	}
	attempt.SourceRefs = withoutDataset(id, "evidence", "4143")
	saved, err := db.Agents().SaveMemory(ctx, scope, attempt, false)
	must(t, err)
	var savedRefs []map[string]any
	must(t, json.Unmarshal(saved.SourceRefs, &savedRefs))
	if _, exists := savedRefs[0]["dataset"]; exists {
		t.Fatal("validator silently rewrote legacy reference", savedRefs)
	}
	// Exact authorized matches are checked, but unknown legacy namespaces stay
	// accepted, including DWS connections absent from platform_connections.
	for _, refs := range []json.RawMessage{withoutDataset("unknown", "evidence", "legacy"), withoutDataset(id, "dws-connection", "dws-native")} {
		_, err = db.Agents().SaveMemory(ctx, scope, f.Memory{Title: "legacy namespace", Content: "unclassified legacy evidence", SourceRefs: refs}, true)
		must(t, err)
	}
}

func TestMemorySourceRefsPostgresRejectsForeignAndInactiveWithoutDisclosure(t *testing.T) {
	db, scope, _ := learnerStageFixture(t)
	ctx := context.Background()
	other := fixtureUser(t, db, "evidence-other", model.RoleUser)
	foreignID := memoryEvidenceFixture(t, db, other.ID, "foreign", "foreign-private-native")
	id := memoryEvidenceFixture(t, db, scope.OwnerID, "own", "own-native")
	for _, refs := range []json.RawMessage{memoryEvidenceRef(foreignID, "foreign", "guess"), memoryEvidenceRef(id, "wrong-connection", "guess")} {
		_, err := db.Agents().SaveMemory(ctx, scope, f.Memory{Title: "reject", Content: "not authorized", SourceRefs: refs}, true)
		code(t, err, "INVALID_ARGUMENT")
		if strings.Contains(err.Error(), "foreign-private-native") || strings.Contains(err.Error(), "own-native") {
			t.Fatal("inaccessible source mapping disclosed the native ID", err)
		}
	}
	for _, change := range []string{`UPDATE platform_connections SET enabled=false WHERE user_id=$1 AND id='own'`, `UPDATE platform_connections SET enabled=true,state='expired' WHERE user_id=$1 AND id='own'`} {
		_, err := db.pool.Exec(ctx, change, scope.OwnerID)
		must(t, err)
		_, err = db.Agents().SaveMemory(ctx, scope, f.Memory{Title: "inactive", Content: "not currently authorized", SourceRefs: memoryEvidenceRef(id, "own", "own-native")}, true)
		code(t, err, "INVALID_ARGUMENT")
	}
}

func TestMemorySourceRefsPostgresStagingAndBootstrapRollback(t *testing.T) {
	db, scope, store := learnerStageFixture(t)
	ctx := context.Background()
	id := memoryEvidenceFixture(t, db, scope.OwnerID, "evidence", "4143")
	commit := initialStage(scope)
	commit.Desired.Memories[0].SourceRefs = memoryEvidenceRef(id, "evidence", "4142")
	_, err := store.CommitLearnerStage(ctx, commit)
	code(t, err, "INVALID_ARGUMENT")
	var workflows, memories, receipts int
	must(t, db.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1),(SELECT count(*) FROM memories WHERE owner_user_id=$1),(SELECT count(*) FROM learner_stage_commits WHERE owner_user_id=$1)`, scope.OwnerID).Scan(&workflows, &memories, &receipts))
	if workflows != 0 || memories != 0 || receipts != 0 {
		t.Fatal("invalid staging reference escaped atomic rollback", workflows, memories, receipts)
	}
	commit.Desired.Memories[0].SourceRefs = memoryEvidenceRef(id, "evidence", "4143")
	_, err = store.CommitLearnerStage(ctx, commit)
	must(t, err)
	bootstrap := db.Agents().(f.WorkflowProfileRepository)
	seed := profileSeed()
	seed.SourceRefs = memoryEvidenceRef(id, "evidence", "4142")
	_, err = bootstrap.CreateWorkflowWithMemories(ctx, scope, f.Workflow{Name: "invalid evidence bootstrap", Definition: json.RawMessage(`{}`)}, []f.Memory{seed})
	code(t, err, "INVALID_ARGUMENT")
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1`, scope.OwnerID).Scan(&workflows))
	if workflows != 1 {
		t.Fatal("invalid bootstrap reference leaked a Workflow", workflows)
	}
	seed.SourceRefs = memoryEvidenceRef(id, "evidence", "4143")
	_, err = bootstrap.CreateWorkflowWithMemories(ctx, scope, f.Workflow{Name: "valid evidence bootstrap", Definition: json.RawMessage(`{}`)}, []f.Memory{seed})
	must(t, err)
}
