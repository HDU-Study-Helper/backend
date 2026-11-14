package main

import (
	"encoding/json"
	"strings"
	"testing"

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
func TestRegressionToolRestrictionCannotGrantExternalActions(t *testing.T) {
	got := filter([]string{"data_query", "agent_dispatch", "notification_send", "schedule_create", "workflow_delete", "workflow_create"}, map[string]bool{"data_query": true, "workflow_create": true})
	if strings.Join(got, ",") != "data_query,workflow_create" {
		t.Fatal(got)
	}
}
