package runcontrol

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

type noticeCandidateData struct {
	calls int
	owner string
	q     f.Query
	page  f.QueryResult
}

func (d *noticeCandidateData) Catalog(context.Context, f.Scope) ([]f.Dataset, error) { return nil, nil }
func (d *noticeCandidateData) Query(_ context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	d.calls++
	d.owner = s.OwnerID
	d.q = q
	return d.page, nil
}
func noticeFixture() (model.ControlChange, map[string]any, map[string]any, map[string]any) {
	change := model.ControlChange{RecordID: "assignment-record", ConnectionID: "connection", Source: "chaoxing", Resource: "assignment"}
	record := map[string]any{"id": "assignment-record", "connection_id": "connection", "source": "chaoxing", "kind": "assignment", "title": "2026 第二章第四次作业20261109"}
	item := map[string]any{"connection_id": "connection", "kind": "assignment", "title": record["title"], "deadline": "", "record_ids": "[\"assignment-record\"]"}
	row := map[string]any{"id": "notice-record", "connection_id": "connection", "source": "chaoxing", "kind": "notice", "title": "作业:《课程》2026 第二章第四次作业20261109", "text": "结束时间：2026-11-15 23:59"}
	return change, record, item, row
}
func TestAssignmentNoticeCandidatesBoundedOwnerScopedDiscovery(t *testing.T) {
	change, record, item, row := noticeFixture()
	row["unexpected_private"] = "excluded"
	d := &noticeCandidateData{page: f.QueryResult{Rows: []map[string]any{row}, HasMore: true}}
	got, err := FindAssignmentNoticeCandidates(context.Background(), d, f.Scope{OwnerID: "owner"}, change, record, item)
	if err != nil || got == nil || got.Count != 1 || !got.HasMore || got.AssociationStatus != "candidate_only_verify_course_and_body_before_use" {
		t.Fatal(got, err)
	}
	if d.owner != "owner" || d.q.Limit != 3 || d.q.Dataset != "platform_records" {
		t.Fatal(d.owner, d.q)
	}
	want := []f.Filter{{Field: "connection_id", Operator: "eq", Value: "connection"}, {Field: "source", Operator: "eq", Value: "chaoxing"}, {Field: "kind", Operator: "eq", Value: "notice"}, {Field: "title", Operator: "contains", Value: item["title"]}}
	if !reflect.DeepEqual(d.q.Filters, want) {
		t.Fatal(d.q.Filters)
	}
	if _, ok := got.Rows[0]["unexpected_private"]; ok {
		t.Fatal("unexpected field copied")
	}
}
func TestAssignmentNoticeCandidatesRejectWrongConnectionAndOversize(t *testing.T) {
	for _, mode := range []string{"wrong-connection", "wrong-source", "oversize", "short-name", "has-deadline", "not-assignment", "wrong-record", "wrong-evidence-link", "missing-owner"} {
		t.Run(mode, func(t *testing.T) {
			change, record, item, row := noticeFixture()
			scope := f.Scope{OwnerID: "owner"}
			switch mode {
			case "wrong-connection":
				row["connection_id"] = "other"
			case "wrong-source":
				row["source"] = "other"
			case "oversize":
				row["text"] = strings.Repeat("x", assignmentNoticeCandidateBytes)
			case "short-name":
				item["title"] = "作业1"
				record["title"] = "作业1"
			case "has-deadline":
				item["deadline"] = "2026-11-15T23:59:00+08:00"
			case "not-assignment":
				item["kind"] = "material"
			case "wrong-record":
				record["id"] = "other"
			case "wrong-evidence-link":
				item["record_ids"] = "[\"other\"]"
			case "missing-owner":
				scope.OwnerID = ""
			}
			d := &noticeCandidateData{page: f.QueryResult{Rows: []map[string]any{row}}}
			got, e := FindAssignmentNoticeCandidates(context.Background(), d, scope, change, record, item)
			if e != nil || got != nil {
				t.Fatal(got, e)
			}
			if mode != "wrong-connection" && mode != "wrong-source" && mode != "oversize" && d.calls != 0 {
				t.Fatal("unnecessary query", d.calls)
			}
		})
	}
}
func TestOptionalNoticeCandidatesNeverDropExistingSnapshot(t *testing.T) {
	base := map[string]any{"record": map[string]any{"id": "kept", "text": strings.Repeat("x", 45<<10)}}
	original, _ := json.Marshal(base)
	base["related_account_notice_candidates"] = map[string]any{"text": strings.Repeat("y", 6<<10)}
	raw, e := marshalEvidenceWithOptionalNotices(base)
	if e != nil || string(raw) != string(original) {
		t.Fatal("existing snapshot lost", len(raw), e)
	}
	if _, ok := base["record"]; !ok {
		t.Fatal("record deleted")
	}
}
