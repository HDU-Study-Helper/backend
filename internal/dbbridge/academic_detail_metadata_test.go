package dbbridge

import (
	"academic/backend/internal/model"
	"encoding/json"
	"strings"
	"testing"
)

func TestAssignmentHeaderAndTeachingDetailsReachProjection(t *testing.T) {
	ref := academicRef("chaoxing", "c", "k", "")
	f := map[string]any{"course_id": "c", "class_id": "k", "start_at_raw": "11-04 09:30", "start_at_source": "assignment_header:作答时间", "end_at_raw": "11-08 23:59", "end_at_source": "assignment_header:作答时间", "deadline_raw": "11-08 23:59", "deadline_source": "assignment_header:作答时间", "answer_time_raw": "11-04 09:30至11-08 23:59", "question_count": 10, "full_score": 100, "status": "未交", "cookie": "PRIVATE"}
	rows := []model.PlatformRecord{academicFixture(1, "chaoxing", "course", ref, "", "数分", map[string]any{"course_id": "c", "class_id": "k"}), academicFixture(2, "chaoxing", "assignment", "work", ref, "作业", f), academicFixture(3, "chaoxing", "lesson", "node", ref, "第一章", map[string]any{"course_id": "c", "class_id": "k", "node_id": "node", "parent_node_id": "parent", "path": []string{"第一章", "极限"}, "path_ids": []string{"parent", "node"}, "task_point_total": 5, "task_point_completed": 3, "task_point_remaining": 2, "personal_status": "unknown"})}
	p, err := buildAcademicProjection("owner", "connection", "chaoxing", rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range p.items {
		if strings.Contains(string(i.Metadata), "PRIVATE") {
			t.Fatal("secret leaked")
		}
		if i.Kind == "assignment" {
			if i.Start.Raw != "11-04 09:30" || i.End.Raw != "11-08 23:59" || i.Deadline.Precision != "month_day_minute" || i.Deadline.Value != nil || i.Published.State != "missing" {
				t.Fatal(i)
			}
			var m map[string]any
			json.Unmarshal(i.Metadata, &m)
			if m["question_count"] != float64(10) || m["full_score"] != float64(100) || m["answer_time_raw"] == nil {
				t.Fatal(m)
			}
		}
		if i.Kind == "lesson" {
			if !strings.Contains(string(i.Metadata), `"parent_node_id":"parent"`) || !strings.Contains(string(i.Metadata), `"path_ids":["parent","node"]`) || !strings.Contains(string(i.PersonalState), `"task_point_completed":3`) {
				t.Fatal(i)
			}
		}
	}
}
