package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
	"github.com/jackc/pgx/v5"
)

func TestSnapshotExcludesPriorExecutionAndPrivateState(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range snapshotTables {
		if seen[name] {
			t.Fatal("duplicate table", name)
		}
		seen[name] = true
	}
	for _, name := range []string{"agent_workflows", "agent_workflow_versions", "memories", "agent_memories", "memory_relations", "agent_runs", "control_batches", "control_items", "control_runs", "control_events", "sessions"} {
		if seen[name] {
			t.Fatal("prior execution state must not enter a fresh-profile regression", name)
		}
	}
	for _, name := range []string{"users", "agent_instances", "agent_framework_settings", "academic_offerings", "academic_items", "academic_item_evidence", "platform_records", "schedules", "dws_groups", "dws_messages"} {
		if !seen[name] {
			t.Fatal("missing context or access prerequisite", name)
		}
	}
	position := map[string]int{}
	for i, name := range snapshotTables {
		position[name] = i
	}
	for _, edge := range [][2]string{{"users", "agent_instances"}, {"platform_connections", "platform_records"}, {"platform_records", "academic_offerings"}, {"courses", "academic_offerings"}, {"academic_offerings", "academic_items"}, {"academic_items", "academic_item_evidence"}, {"dws_groups", "dws_messages"}, {"dws_messages", "dws_attachments"}} {
		if position[edge[0]] >= position[edge[1]] {
			t.Fatal("foreign-key copy order", edge)
		}
	}
}
func TestCaseInputPreservesOpaqueCourseIdentity(t *testing.T) {
	c := course{Owner: "owner", Connection: "connection", Source: "chaoxing", ID: "offering", Ref: `["course","class"]`, Name: "Course", Record: "123"}
	raw, err := json.Marshal(caseInput(c))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Context struct {
			RequestType string `json:"request_type"`
			CourseRef   string `json:"course_ref"`
			Entity      struct {
				ID string `json:"id"`
			} `json:"entity"`
		} `json:"context"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Context.RequestType != "CREATE" || p.Context.CourseRef != c.Ref || p.Context.Entity.ID != "source=chaoxing|connection=connection|course_ref="+c.Ref {
		t.Fatal("case changed explicit identity", string(raw))
	}
}
func TestGeneratedSchemaCannotSelectProduction(t *testing.T) {
	a, err := newSchema()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSchema()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) > 63 || !identifier.MatchString(a) || !strings.HasPrefix(a, "learner_regression_") {
		t.Fatal(a, b)
	}
	for _, bad := range []string{"public,pg_catalog", `public";DROP SCHEMA public`, "a.b", ""} {
		if identifier.MatchString(bad) {
			t.Fatal("unsafe schema identifier", bad)
		}
	}
}
func TestIsolatedDSNOverridesOriginalSearchPath(t *testing.T) {
	const schema = "learner_regression_20261114_010000_0123456789abcdef"
	for _, input := range []string{
		"postgres://user:password@localhost/db?sslmode=disable&search_path=public&default_transaction_read_only=on",
		"host=localhost user=user password='with space' dbname=db search_path=public default_transaction_read_only=on",
	} {
		dsn, err := isolatedDSN(input, schema)
		if err != nil {
			t.Fatal("cannot isolate DSN", err)
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil || cfg.RuntimeParams["search_path"] != schema || cfg.RuntimeParams["default_transaction_read_only"] != "off" {
			t.Fatal("target parser did not select isolated schema")
		}
	}
	if _, err := isolatedDSN("postgres://localhost/db", "public"); err == nil {
		t.Fatal("production target accepted")
	}
}
func TestClosureRequiresExplicitExecutionAndFrozenResumeSelection(t *testing.T) {
	for _, o := range []options{
		{parallel: 1, prepareOnly: true},
		{parallel: 1, closureStage: "tools"},
		{parallel: 1, execute: true, closureStage: "unknown"},
		{parallel: 1, execute: true, closureStage: "tools", resumeDir: "does-not-exist", offering: "changed"},
	} {
		if err := run(context.Background(), o); err == nil {
			t.Fatal("unsafe closure options accepted", o)
		}
	}
}

func TestClosureWorkerAcceptanceRejectsInventedIdentity(t *testing.T) {
	valid := `{"run_id":"run","workflow_id":"wf","workflow_version":1,"status":"COMPLETED","evidence_refs":["record"],"artifacts":[{"type":"SUMMARY","title":"course","description":"sourced answer","data":{"record_id":"record"}}]}`
	if err := closureWorkerResult(valid, "run", "wf", 1); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{strings.Replace(valid, `"run_id":"run"`, `"run_id":"invented"`, 1), strings.Replace(valid, `"workflow_id":"wf"`, `"workflow_id":"other"`, 1), strings.Replace(valid, `"workflow_version":1`, `"workflow_version":2`, 1), strings.Replace(valid, `"evidence_refs":["record"]`, `"evidence_refs":[]`, 1), strings.Replace(valid, `"status":"COMPLETED"`, `"status":"PARTIAL"`, 1)} {
		if closureWorkerResult(value, "run", "wf", 1) == nil {
			t.Fatal("false Worker completion accepted", value)
		}
	}
}

type closureRevisionFixture struct {
	runs   map[string]f.RunRecord
	ledger stage.Receipt
}

func (s closureRevisionFixture) GetRun(_ context.Context, owner, id string) (f.RunRecord, error) {
	v, ok := s.runs[id]
	if !ok || v.OwnerID != owner {
		return f.RunRecord{}, fmt.Errorf("run not found")
	}
	return v, nil
}
func (s closureRevisionFixture) LearnerStageReceipt(_ context.Context, owner, id string) (stage.Receipt, error) {
	if owner != "owner" || id != s.ledger.SessionID {
		return stage.Receipt{}, fmt.Errorf("receipt not found")
	}
	return s.ledger, nil
}

func TestClosureRevisionRequiresActualCommittedLearnerChange(t *testing.T) {
	for _, mode := range []string{"memory", "definition", "partial_investigation", "nochange", "blocked", "fake_owner", "wrong_child", "missing_dispatch", "ledger_mismatch", "no_diff", "worker_only_change", "startup_version"} {
		t.Run(mode, func(t *testing.T) {
			before := stage.Snapshot{Workflow: f.Workflow{ID: "wf", OwnerID: "owner", Name: "course", Status: "active", Version: 1, Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course","name":"Course"}}`)}, Memories: []f.Memory{{ID: "memory", OwnerID: "owner", WorkflowID: "wf", Visibility: "PRI", Title: "fact", Content: "old", SourceRefs: json.RawMessage(`["record"]`)}}, Relations: []f.MemoryRelation{}}
			after := stage.Clone(before)
			after.Memories[0].Content = "new verified fact"
			worker := f.RunRecord{ID: "worker-run", OwnerID: "owner", AgentID: "worker", WorkflowID: "wf", WorkflowVersion: 1, Status: "succeeded", ToolExecutions: []f.ToolExecution{{Name: "agent_dispatch"}}}
			child := f.RunRecord{ID: "learner-run", OwnerID: "owner", AgentID: "learner", ParentRunID: worker.ID, DispatcherID: "worker", WorkflowID: "wf", WorkflowVersion: 1, Trigger: f.AgentDispatch, Status: "succeeded", ToolExecutions: []f.ToolExecution{{Name: "memory_update", ResultRefs: []f.ToolResultRef{{Kind: "MEMORY", ID: "memory"}}}}}
			receipt := stage.Receipt{SessionID: "session", Revision: 3, Operation: "UPDATE", Status: "COMMITTED", WorkflowID: "wf", WorkflowVersion: 1, MemoryIDs: []string{"memory"}, IDMap: map[string]string{"memory": "memory"}, Verified: true}
			status := "APPLIED"
			operation := "UPDATE"
			workerVersion := 1
			if mode == "definition" {
				after.Workflow.Definition = json.RawMessage(`{"entity":{"kind":"course","id":"course","name":"Course"},"goal":"revised"}`)
				after.Workflow.Version = 2
				receipt.WorkflowVersion = 2
				child.ToolExecutions = append(child.ToolExecutions, f.ToolExecution{Name: "workflow_update"})
			}
			if mode == "partial_investigation" || mode == "nochange" || mode == "blocked" || mode == "no_diff" {
				after = stage.Clone(before)
			}
			if mode == "nochange" {
				status = "NO_CHANGE"
				operation = "NONE"
				receipt.Operation = "NONE"
				receipt.Status = "NO_CHANGE"
			}
			if mode == "blocked" {
				status = "NEEDS_INFO"
				receipt = stage.Receipt{}
			}
			if mode == "fake_owner" {
				child.OwnerID = "other"
			}
			if mode == "wrong_child" {
				child.AgentID = "worker"
			}
			if mode == "missing_dispatch" {
				worker.ToolExecutions = nil
			}
			if mode == "worker_only_change" {
				child.ToolExecutions = []f.ToolExecution{{Name: "workflow_get"}}
			}
			if mode == "startup_version" {
				workerVersion = 2
			}
			final := string(rawJSON(map[string]any{"run_id": worker.ID, "workflow_id": "wf", "workflow_version": workerVersion, "status": "PARTIAL", "evolution_feedback": []any{map[string]any{"status": "APPLIED"}}}))
			text := string(rawJSON(map[string]any{"status": status, "operation": operation, "workflow_id": "wf", "workflow_version": receipt.WorkflowVersion, "verified": true, "commit_receipt": receipt}))
			terminals := map[string]map[string]any{child.ID: {"record": child, "output": text}}
			if mode == "partial_investigation" {
				terminals = map[string]map[string]any{}
				worker.ToolExecutions = nil
			}
			fixture := closureRevisionFixture{runs: map[string]f.RunRecord{child.ID: child}, ledger: receipt}
			if mode == "ledger_mismatch" {
				fixture.ledger.Revision++
			}
			audit, err := closureRevisionAcceptance(context.Background(), fixture, fixture, "owner", "worker", "learner", worker, final, before, after, terminals)
			expectPass := mode == "memory" || mode == "definition"
			if expectPass {
				if err != nil || !audit.RevisionPass || audit.InvestigationOnly || len(audit.Commits) != 1 || audit.RequiresFreshRun != (mode == "definition") {
					t.Fatalf("valid committed revision rejected: %+v %v", audit, err)
				}
			} else {
				if err == nil || audit.RevisionPass {
					t.Fatalf("unproved revision accepted: %+v %v", audit, err)
				}
			}
			if mode == "partial_investigation" || mode == "nochange" || mode == "blocked" {
				if !audit.InvestigationOnly || audit.Disposition != "investigation_only" {
					t.Fatal("investigation lost its separate outcome", audit)
				}
			}
		})
	}
}

func TestRegressionToolRestrictionCannotGrantExternalActions(t *testing.T) {
	got := filter([]string{"data_query", "agent_dispatch", "notification_send", "schedule_create", "workflow_delete", "workflow_create"}, map[string]bool{"data_query": true, "workflow_create": true})
	if strings.Join(got, ",") != "data_query,workflow_create" {
		t.Fatal(got)
	}
}
