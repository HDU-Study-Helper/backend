package mooc

import (
	"academic/backend/internal/contract"
	"encoding/json"
	"testing"
)

func TestDiscussionTaskMarkerDoesNotClaimReply(t *testing.T) {
	for _, tc := range []struct {
		name      string
		value     any
		present   bool
		status    string
		completed any
	}{{"green", json.Number("1"), true, "completed", true}, {"grey", json.Number("0"), true, "incomplete", false}, {"bool green", true, true, "completed", true}, {"missing", nil, false, "unknown", nil}, {"server learned code five", json.Number("5"), true, "completed", true}, {"string zero not JS false", "0", true, "unknown", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			f := map[string]any{"contentType": json.Number("6"), "countReply": 99}
			if tc.present {
				f["viewStatus"] = tc.value
			}
			row := discussionUnitRecord(contract.Record{Kind: contract.Discussion, ExternalID: "root", ParentID: "course", Content: "Teacher requirements", Fields: f})
			if row.Fields["task_point_status"] != tc.status || row.Fields["task_point_completed"] != tc.completed || row.Fields["participation_status"] != "unknown" {
				t.Fatal(row.Fields)
			}
			if _, ok := row.Fields["is_current_user"]; ok {
				t.Fatal("view marker became verified reply identity")
			}
			if row.Content != "Teacher requirements" || row.ParentID != "course" {
				t.Fatal("root changed", row)
			}
			if row.Fields["reply_collection_policy"] != "disabled_current_user_task_state_only" {
				t.Fatal("roster policy missing")
			}
		})
	}
}

func TestLegacyDiscussionRetirementUsesCatalogSnapshot(t *testing.T) {
	for _, snapshot := range []map[string]any{{"viewStatus": json.Number("0"), "replyCount": nil}, nil} {
		f := map[string]any{"source_detail_endpoint": "PostBean.getPostDetailById", "viewStatus": json.Number("5"), "replyCount": json.Number("1"), "current_user_reply_count": 8, "current_user_participation_fields": snapshot, "is_current_user": true, "task_point_completed": true}
		row := discussionUnitRecord(contract.Record{Kind: contract.Discussion, Fields: f})
		if row.Fields["participation_status"] != "unknown" {
			t.Fatal("detail became catalog reply evidence", row.Fields)
		}
		if _, ok := row.Fields["current_user_reply_count"]; ok {
			t.Fatal("stale count survived")
		}
		if snapshot == nil {
			if row.Fields["task_point_status"] != "unknown" {
				t.Fatal("missing catalog snapshot guessed")
			}
		} else if row.Fields["task_point_completed"] != false {
			t.Fatal("catalog false lost")
		}
		if _, ok := row.Fields["is_current_user"]; ok {
			t.Fatal("poster flag survived")
		}
	}
}

func TestCatalogDirectDiscussionTaskStatusNeedsNoReplyUsers(t *testing.T) {
	dto := fixtureDTO(t, `{"mocTermDto":{"id":101,"chapters":[{"id":11,"lessons":[{"id":12,"units":[{"id":13,"contentId":14,"name":"Discussion","contentType":6,"viewStatus":5,"replyCount":1},{"id":15,"contentId":16,"name":"Other discussion","contentType":6,"viewStatus":0}]}]}]}}`)
	for _, resource := range []contract.Resource{contract.Discussion, contract.Progress} {
		rows, e := termRecords(dto, course{ID: "10", Term: "101", Type: 1}, resource)
		if e != nil || len(rows) != 2 {
			t.Fatal(e, rows)
		}
		if rows[0].Fields["task_point_status"] != "completed" || rows[1].Fields["task_point_status"] != "incomplete" {
			t.Fatal(rows)
		}
		if rows[0].Fields["participation_status"] != "participated" || rows[0].Fields["current_user_reply_count"] != int64(1) || rows[1].Fields["participation_status"] != "unknown" {
			t.Fatal("personal catalog count must remain distinct from task marker and missing count")
		}
	}
}
