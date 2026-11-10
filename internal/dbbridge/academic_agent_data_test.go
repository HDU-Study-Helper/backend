package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"strings"
	"testing"
	"time"
)

func TestAcademicDatasetFieldsAreExplicitAndTyped(t *testing.T) {
	for name, set := range academicDatasets() {
		seen := map[string]bool{}
		for _, field := range set.names {
			if seen[field] || set.fields[field] == "" {
				t.Fatalf("%s invalid catalog field %s", name, field)
			}
			seen[field] = true
		}
		if len(seen) != len(set.fields) {
			t.Fatalf("%s catalog mismatch", name)
		}
		for _, field := range []string{"cookie", "token", "session", "password", "raw", "fields"} {
			if _, ok := set.fields[field]; ok {
				t.Fatalf("unsafe field %s", field)
			}
		}
	}
	metadata := academicDatasets()["academic_items"].fields["metadata"]
	if !strings.Contains(metadata, "jsonb_build_object") || !strings.Contains(metadata, "jsonb_typeof") || strings.Contains(metadata, "a.metadata::text") || strings.Contains(metadata, "'token'") {
		t.Fatal("metadata must expose only typed explicit fields", metadata)
	}
	if !academicDatasets()["academic_items"].times["deadline"] {
		t.Fatal("deadline comparisons must be typed")
	}
}
func TestAcademicStructuredAgentQueryFiltersAndOwnerScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "academic-query", model.RoleUser)
	other := fixtureUser(t, db, "academic-other", model.RoleUser)
	_, err := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,'cx','chaoxing','active'),($2,'cx','chaoxing','active')`, owner.ID, other.ID)
	must(t, err)
	for _, who := range []string{owner.ID, other.ID} {
		_, err = db.pool.Exec(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,'cx','chaoxing','course','["course","class"]','','hash','{"title":"数学","fields":{"course_id":"course","class_id":"class"}}',now()),($1,'cx','chaoxing','assignment','task','["course","class"]','task-hash','{"title":"实验报告","content":"报告要求","fields":{"course_id":"course","class_id":"class","deadline":"2026-11-15T10:00:00+08:00","status":"未交","cookie":"private-value"}}',now())`, who)
		must(t, err)
		_, err = db.Academic().Rebuild(ctx, who, "chaoxing")
		must(t, err)
	}
	result, err := db.AgentData().Query(ctx, f.Scope{OwnerID: owner.ID}, f.Query{Dataset: "academic_items", Fields: []string{"id", "course_name", "deadline", "status", "record_ids", "missing"}, Filters: []f.Filter{{Field: "deadline", Operator: "gte", Value: "2026-11-15T01:59:59Z"}}, Limit: 100})
	must(t, err)
	if len(result.Rows) != 1 || result.Rows[0]["course_name"] != "数学" || result.Rows[0]["status"] != "open" {
		t.Fatalf("wrong structured rows %#v", result.Rows)
	}
	deadline, _ := result.Rows[0]["deadline"].(string)
	if _, err = time.Parse(time.RFC3339, deadline); err != nil {
		t.Fatal("deadline is not ISO instant", deadline)
	}
	if strings.Contains(result.Rows[0]["missing"].(string), "private-value") {
		t.Fatal("private source field leaked")
	}
	_, err = db.AgentData().Query(ctx, f.Scope{OwnerID: owner.ID}, f.Query{Dataset: "academic_items", Fields: []string{"id"}, Filters: []f.Filter{{Field: "deadline", Operator: "gte", Value: "11-15 10:00"}}, Limit: 1})
	if err == nil {
		t.Fatal("partial date accepted as instant filter")
	}
	_, err = db.pool.Exec(ctx, `UPDATE platform_connections SET enabled=false WHERE user_id=$1`, owner.ID)
	must(t, err)
	result, err = db.AgentData().Query(ctx, f.Scope{OwnerID: owner.ID}, f.Query{Dataset: "academic_items", Fields: []string{"id"}, Limit: 100})
	must(t, err)
	if len(result.Rows) != 0 {
		t.Fatal("disabled owner data exposed or crossed owners")
	}
}
