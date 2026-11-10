package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func profileSeed() f.Memory {
	return f.Memory{Title: "course profile", Content: "PRIVATE_COURSE_SNAPSHOT", Category: "profile_index", SourceRefs: json.RawMessage(`[{"dataset":"academic_offerings","id":"course-1"}]`)}
}

func TestWorkflowProfileSeedValidationDoesNotElevateMemoryScope(t *testing.T) {
	base := profileSeed()
	normalized, err := normalizeWorkflowProfileSeed(base)
	must(t, err)
	if normalized.Visibility != "PRI" || normalized.AgentID != "" || normalized.WorkflowID != "" || normalized.MemoryType != "BASE" {
		t.Fatal(normalized)
	}
	for _, change := range []func(*f.Memory){
		func(m *f.Memory) { m.OwnerID = "foreign" }, func(m *f.Memory) { m.AgentID = "agent" }, func(m *f.Memory) { m.WorkflowID = "workflow" },
		func(m *f.Memory) { m.ID = "existing" }, func(m *f.Memory) { m.ParentID = "parent" }, func(m *f.Memory) { m.Visibility = "PUB" },
		func(m *f.Memory) { m.Status = "archived" }, func(m *f.Memory) { m.SourceRefs = nil }, func(m *f.Memory) { m.SourceRefs = json.RawMessage(`[]`) },
		func(m *f.Memory) { m.SourceRefs = json.RawMessage(`[null]`) }, func(m *f.Memory) { m.SourceRefs = json.RawMessage(`[{}]`) },
		func(m *f.Memory) { m.SourceRefs = json.RawMessage(`[""]`) }, func(m *f.Memory) { m.SourceRefs = json.RawMessage(`[123]`) },
	} {
		seed := base
		change(&seed)
		_, err := normalizeWorkflowProfileSeed(seed)
		code(t, err, "INVALID_ARGUMENT")
	}
	ordinary := base
	ordinary.WorkflowID = "unbound"
	code(t, normalizeMemory(f.Scope{OwnerID: "owner", AgentID: "learner"}, &ordinary), "PERMISSION_DENIED")
}

func TestWorkflowProfileAtomicCreationIsolationPermissionsAndRollback(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "profile-owner", model.RoleUser)
	v := fixtureUser(t, db, "profile-foreign", model.RoleUser)
	repo := db.Agents()
	registry := f.Registry{Repository: repo}
	createAgent := func(owner, name string, permissions []string) f.AgentInstance {
		a, e := registry.Create(ctx, owner, definition(name, "mock", []string{"workflow_create"}, permissions))
		must(t, e)
		return a
	}
	learner := createAgent(u.ID, "learner", []string{"workflow.write", "memory.write"})
	worker := createAgent(u.ID, "worker", []string{"memory.read"})
	other := createAgent(v.ID, "other", []string{"memory.read"})
	scope := f.Scope{OwnerID: u.ID, AgentID: learner.ID}
	bootstrap := repo.(f.WorkflowProfileRepository)
	result, err := bootstrap.CreateWorkflowWithMemories(ctx, scope, f.Workflow{Name: "course", Definition: json.RawMessage(`{}`)}, []f.Memory{profileSeed(), {Title: "assignment", Content: "assignment details", Category: "assignments", SourceRefs: json.RawMessage(`["academic_items:item-1"]`)}})
	must(t, err)
	if scope.WorkflowID != "" || !result.ProfileInitialization.Verified || result.ProfileInitialization.Count != 2 || len(result.ProfileInitialization.ContentSHA256) != 64 {
		t.Fatal(result)
	}
	versions, err := repo.WorkflowVersions(ctx, u.ID, result.ID)
	must(t, err)
	if len(versions) != 1 {
		t.Fatal(versions)
	}
	for _, id := range result.ProfileInitialization.MemoryIDs {
		memory, err := repo.GetMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: worker.ID, WorkflowID: result.ID}, id)
		must(t, err)
		if memory.Visibility != "PRI" || memory.WorkflowID != result.ID || memory.AgentID != "" {
			t.Fatal(memory)
		}
		for _, s := range []f.Scope{scope, {OwnerID: u.ID, AgentID: worker.ID}, {OwnerID: v.ID, AgentID: other.ID}} {
			_, err = repo.GetMemory(ctx, s, id)
			code(t, err, "RESOURCE_NOT_FOUND")
		}
	}
	for _, permissions := range [][]string{{"workflow.write"}, {"memory.write"}} {
		a := createAgent(u.ID, "limited", permissions)
		_, err = bootstrap.CreateWorkflowWithMemories(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, f.Workflow{Name: "denied", Definition: json.RawMessage(`{}`)}, []f.Memory{profileSeed()})
		code(t, err, "PERMISSION_DENIED")
	}
	noTool := createAgent(u.ID, "no-tool", []string{"workflow.write", "memory.write"})
	noTool.EnabledTools = nil
	noTool, err = registry.Save(ctx, u.ID, noTool)
	must(t, err)
	_, err = bootstrap.CreateWorkflowWithMemories(ctx, f.Scope{OwnerID: u.ID, AgentID: noTool.ID}, f.Workflow{Name: "denied", Definition: json.RawMessage(`{}`)}, []f.Memory{profileSeed()})
	code(t, err, "PERMISSION_DENIED")
	learner.Status = "disabled"
	learner, err = registry.Save(ctx, u.ID, learner)
	must(t, err)
	_, err = bootstrap.CreateWorkflowWithMemories(ctx, scope, f.Workflow{Name: "denied", Definition: json.RawMessage(`{}`)}, []f.Memory{profileSeed()})
	code(t, err, "PERMISSION_DENIED")
	learner.Status = "enabled"
	_, err = registry.Save(ctx, u.ID, learner)
	must(t, err)
	// A failure after the workflow/version and first memory have been inserted
	// must roll them all back. The trigger is confined to this test's schema.
	_, err = db.pool.Exec(ctx, `CREATE FUNCTION fail_profile_seed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.title='force rollback' THEN RAISE EXCEPTION 'fixture failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER profile_failure BEFORE INSERT ON memories FOR EACH ROW EXECUTE FUNCTION fail_profile_seed();`)
	must(t, err)
	bad := profileSeed()
	bad.Title = "force rollback"
	failed, err := bootstrap.CreateWorkflowWithMemories(ctx, scope, f.Workflow{Name: "rollback-profile", Definition: json.RawMessage(`{}`)}, []f.Memory{profileSeed(), bad})
	code(t, err, "INVALID_ARGUMENT")
	if failed.ID != "" || failed.ProfileInitialization.Verified || len(failed.ProfileInitialization.MemoryIDs) != 0 {
		t.Fatal("failed write leaked receipt", failed)
	}
	var workflows, versionsCount, memories int
	must(t, db.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1),(SELECT count(*) FROM agent_workflow_versions WHERE owner_user_id=$1),(SELECT count(*) FROM memories WHERE owner_user_id=$1)`, u.ID).Scan(&workflows, &versionsCount, &memories))
	if workflows != 1 || versionsCount != 1 || memories != 2 {
		t.Fatalf("rollback left records: %d/%d/%d", workflows, versionsCount, memories)
	}
}

func TestWorkflowProfileActiveDuplicateDoesNotAddSeeds(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "profile-duplicate", model.RoleUser)
	repo := db.Agents()
	a, err := (f.Registry{Repository: repo}).Create(ctx, u.ID, definition("learner", "mock", []string{"workflow_create"}, []string{"workflow.write", "memory.write"}))
	must(t, err)
	bootstrap := repo.(f.WorkflowProfileRepository)
	scope := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	w := f.Workflow{Name: "course profile", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course-1","name":"工科数学分析"},"goal":"track course"}`)}
	var def map[string]any
	json.Unmarshal(w.Definition, &def)
	manifest := map[string]any{"version": 1, "keywords": []string{"数学分析"}, "search_coverage": []any{}, "dimensions": map[string]any{}}
	for _, dataset := range []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"} {
		manifest["search_coverage"] = append(manifest["search_coverage"].([]any), map[string]any{"dataset": dataset, "complete": true})
	}
	for _, dim := range []string{"identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation"} {
		manifest["dimensions"].(map[string]any)[dim] = map[string]any{"status": "partial"}
	}
	def["profile_manifest"] = manifest
	w.Definition, _ = json.Marshal(def)
	seeds := []f.Memory{profileSeed(), {Title: "identity", Content: "verified identity", Category: "identity", SourceRefs: json.RawMessage(`["academic_offerings:course-1"]`)}}
	first, err := bootstrap.CreateWorkflowWithMemories(ctx, scope, w, seeds)
	must(t, err)
	second, err := bootstrap.CreateWorkflowWithMemories(ctx, scope, w, seeds)
	code(t, err, "CONFLICT")
	if first.ID == "" || second.ID != "" || second.ProfileInitialization.Verified {
		t.Fatal(first, second)
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE owner_user_id=$1`, u.ID).Scan(&count))
	if count != 2 {
		t.Fatal("duplicate attempt added private seeds", count)
	}
}

func TestWorkflowProfileRejectsAggregateBudgetBeforeDatabaseAccess(t *testing.T) {
	r := &agents{}
	seed := profileSeed()
	seed.Content = strings.Repeat("x", 262144)
	_, err := r.CreateWorkflowWithMemories(context.Background(), f.Scope{}, f.Workflow{}, []f.Memory{seed, seed, seed, seed, seed})
	code(t, err, "INVALID_ARGUMENT")
}
