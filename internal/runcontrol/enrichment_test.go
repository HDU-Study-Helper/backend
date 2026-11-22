package runcontrol

import (
	"context"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
)

type enrichmentData struct {
	queries []f.Query
	scopes  []f.Scope
	missing bool
}

func (d *enrichmentData) Catalog(_ context.Context, s f.Scope) ([]f.Dataset, error) {
	d.scopes = append(d.scopes, s)
	return []f.Dataset{{Name: "platform_records", Fields: []f.Field{{Name: "id"}, {Name: "connection_id"}, {Name: "source"}, {Name: "kind"}, {Name: "external_id"}, {Name: "parent_id"}, {Name: "title"}, {Name: "text"}}}}, nil
}
func (d *enrichmentData) Query(_ context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	d.scopes = append(d.scopes, s)
	d.queries = append(d.queries, q)
	if d.missing {
		return f.QueryResult{}, nil
	}
	if q.Filters[0].Field == "id" {
		return f.QueryResult{Rows: []map[string]any{{"id": "10", "connection_id": "connection", "source": "chaoxing", "kind": "assignment", "external_id": "task", "parent_id": "[\"course\",\"class\"]", "title": "task"}}}, nil
	}
	return f.QueryResult{Rows: []map[string]any{{"id": "9", "connection_id": "connection", "source": "chaoxing", "kind": "course", "external_id": "[\"course\",\"class\"]", "title": "具体课程"}}}, nil
}

type enrichmentRepo struct {
	f.Repository
	workflowLists int
}

func (r *enrichmentRepo) ListWorkflows(_ context.Context, _ string, _ int, _ string) ([]f.Workflow, error) {
	r.workflowLists++
	return []f.Workflow{}, nil
}
func TestEnrichmentQueriesExactOwnerRecordAndOpaqueCourse(t *testing.T) {
	data := &enrichmentData{}
	repo := &enrichmentRepo{}
	s := &Service{Data: data, Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
	b := model.ControlBatch{OwnerID: "owner"}
	change := model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"}
	ctx := s.enrichItemContext(context.Background(), b, change)
	if len(data.queries) != 2 || repo.workflowLists != 0 {
		t.Fatal(data.queries, "unexpected Workflow preselection queries:", repo.workflowLists)
	}
	for _, scope := range data.scopes {
		if scope.OwnerID != "owner" {
			t.Fatal("wrong owner", scope)
		}
	}
	q := data.queries[0]
	if q.Filters[0].Field != "id" || q.Filters[0].Value != "10" || q.Filters[1].Value != "connection" {
		t.Fatal(q)
	}
	q = data.queries[1]
	if q.Filters[2].Value != `["course","class"]` || q.Filters[1].Value != "course" {
		t.Fatal("opaque parent modified", q)
	}
	refs := knownEvidenceReferences(ctx)
	if !refs["9"] || !refs[`["course","class"]`] {
		t.Fatal("missing verified course refs", refs)
	}
}

type noticeEnrichmentData struct {
	*enrichmentData
	record  map[string]any
	courses []map[string]any
	pages   map[int][]map[string]any
}

func (d *noticeEnrichmentData) Query(_ context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	d.scopes = append(d.scopes, s)
	d.queries = append(d.queries, q)
	if q.Filters[0].Field == "id" {
		return f.QueryResult{Rows: []map[string]any{d.record}}, nil
	}
	if d.pages != nil {
		return f.QueryResult{Rows: d.pages[q.Offset]}, nil
	}
	return f.QueryResult{Rows: d.courses}, nil
}
func TestNoticeCourseExplicitTitleExactUnique(t *testing.T) {
	course := func(id, title, connection string) map[string]any {
		return map[string]any{"id": id, "connection_id": connection, "source": "chaoxing", "kind": "course", "external_id": "[\"course\",\"" + id + "\"]", "title": title}
	}
	for _, tc := range []struct {
		name, title, courseName string
		courses                 []map[string]any
		want                    bool
	}{
		{"brackets and spaces", "《工科数学分析1 （H）（第6期）》课程通知", "", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "connection")}, true},
		{"explicit course name", "普通通知", "工科数学分析1 （H）（第6期）", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "connection")}, true},
		{"ambiguous classes", "《工科数学分析1 （H）（第6期）》通知", "", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "connection"), course("8", "工科数学分析1 (H)（第6期）", "connection")}, false},
		{"other connection", "《工科数学分析1 （H）（第6期）》通知", "", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "foreign")}, false},
		{"unrelated text", "请阅读工科数学分析1 (H)（第6期）的资料", "", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "connection")}, false},
		{"first title only", "《不存在课程》与《工科数学分析1 （H）（第6期）》", "", []map[string]any{course("9", "工科数学分析1 (H)（第6期）", "connection")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &noticeEnrichmentData{enrichmentData: &enrichmentData{}, record: map[string]any{"id": "10", "connection_id": "connection", "source": "chaoxing", "kind": "notice", "external_id": "notice-id", "title": tc.title, "course_name": tc.courseName}, courses: tc.courses}
			s := &Service{Data: d, Agents: &agent.Service{Registry: f.Registry{Repository: &enrichmentRepo{}}}}
			ctx := s.enrichItemContext(context.Background(), model.ControlBatch{OwnerID: "owner"}, model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"})
			if got := len(knownEvidenceReferences(ctx)) > 0; got != tc.want {
				t.Fatalf("linked=%v want=%v", got, tc.want)
			}
			for _, scope := range d.scopes {
				if scope.OwnerID != "owner" {
					t.Fatal("cross-owner query")
				}
			}
			for _, q := range d.queries[1:] {
				if q.Filters[0].Value != "connection" || q.Filters[1].Value != "course" {
					t.Fatal("unscoped course query", q)
				}
			}
		})
	}
}

func TestNoticeCourseChecksLaterPageForAmbiguity(t *testing.T) {
	candidate := map[string]any{"id": "9", "connection_id": "connection", "source": "chaoxing", "kind": "course", "external_id": "course-class-1", "title": "工科数学分析1 (H)（第6期）"}
	first := make([]map[string]any, 100)
	for i := range first {
		first[i] = map[string]any{"id": "other", "connection_id": "connection", "source": "chaoxing", "kind": "course", "external_id": "other", "title": "其他课程"}
	}
	first[0] = candidate
	for _, ambiguous := range []bool{false, true} {
		pages := map[int][]map[string]any{0: first}
		if ambiguous {
			pages[100] = []map[string]any{{"id": "8", "connection_id": "connection", "source": "chaoxing", "kind": "course", "external_id": "course-class-2", "title": candidate["title"]}}
		}
		d := &noticeEnrichmentData{enrichmentData: &enrichmentData{}, pages: pages}
		s := &Service{Data: d}
		course, _ := s.noticeCourse(context.Background(), f.Scope{OwnerID: "owner"}, model.ControlChange{ConnectionID: "connection", Source: "chaoxing"}, map[string]any{"title": "《工科数学分析1 （H）（第6期）》通知"})
		if (course != nil) == ambiguous {
			t.Fatalf("ambiguity=%v course=%v", ambiguous, course)
		}
		if len(d.queries) != 2 || d.queries[1].Offset != 100 {
			t.Fatal("did not exhaust authorized course pages", d.queries)
		}
	}
}

func TestEnrichmentMissingRecordDoesNotInferCourse(t *testing.T) {
	data := &enrichmentData{missing: true}
	s := &Service{Data: data}
	ctx := s.enrichItemContext(context.Background(), model.ControlBatch{OwnerID: "owner"}, model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"})
	if len(data.queries) != 1 || len(knownEvidenceReferences(ctx)) != 0 {
		t.Fatal("invented course context")
	}
}
