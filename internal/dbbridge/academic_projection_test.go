package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

func academicFixture(id int64, source, kind, external, parent, title string, fields map[string]any) model.PlatformRecord {
	content := academicJSON(map[string]any{"title": title, "content": "fixture body", "fields": fields})
	hash := sha256.Sum256(content)
	return model.PlatformRecord{ID: id, Source: source, Kind: kind, ExternalID: external, ParentID: parent, Content: content, Hash: hex.EncodeToString(hash[:]), ObservedAt: time.Date(2026, 11, 4, 10, 0, int(id), 0, time.UTC)}
}
func TestAcademicProjectionIdentityAndEvidence(t *testing.T) {
	ref := academicRef("mooc", "course", "", "owner-term")
	fields := func(extra map[string]any) map[string]any {
		f := map[string]any{"courseId": "course", "termId": "owner-term", "unitId": "unit", "sourceTermId": "reference-term", "source_scope": "spoc_reference", "path": []string{"chapter", "lesson", "unit"}}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	rows := []model.PlatformRecord{
		academicFixture(1, "mooc", "course", ref, "", "Course", fields(map[string]any{"source_terms": []string{"reference-term"}, "coverage": map[string]any{"state": "partial", "token": "must not escape"}})),
		academicFixture(2, "mooc", "assignment", "native-task", ref, "Task", fields(map[string]any{"task_type": "quiz", "submitEndTime": 1800000000123, "cookie": "must not escape", "access_token": "must not escape"})),
		academicFixture(3, "mooc", "progress", "native-task", ref, "", fields(map[string]any{"task_type": "quiz", "usedTryCount": 2, "submitStatus": "submitted", "studentScore": 0})),
		academicFixture(4, "mooc", "progress", "native-last", ref, "", fields(map[string]any{"lastLearnUnitId": "unit"})),
		academicFixture(5, "mooc", "exam", "native-discussion", ref, "Topic", fields(map[string]any{"task_type": "exam", "contentType": 6, "usedTryCount": 0})),
		academicFixture(6, "mooc", "discussion", "native-discussion", ref, "Topic updated", fields(map[string]any{"contentType": 6, "source_collection": "units"})),
	}
	p, err := buildAcademicProjection("owner", "connection", "mooc", rows)
	must(t, err)
	if len(p.offerings) != 1 || len(p.items) != 3 {
		t.Fatalf("expected one offering and 3 logical items: %+v", p)
	}
	if strings.Contains(string(p.offerings[0].Coverage), "token") || !strings.Contains(string(p.offerings[0].SourceTerms), "reference-term") {
		t.Fatal("coverage safety or source relation lost")
	}
	for _, item := range p.items {
		if item.OfferingID != p.offerings[0].ID || item.NativeTermID != "owner-term" || item.SourceTermID != "reference-term" || len(item.Path) != 3 {
			t.Fatalf("explicit relationship lost: %+v", item)
		}
		if strings.Contains(string(item.Metadata), "must not escape") || strings.Contains(string(item.PersonalState), "must not escape") {
			t.Fatal("credentials entered projection")
		}
		switch item.NativeID {
		case "native-task":
			if item.Kind != "assignment" || item.Title != "Task" || item.Status != "submitted" || len(item.RecordIDs) != 2 || len(p.evidence[item.ID]) != 2 || item.Deadline.Value == nil {
				t.Fatalf("task/progress failed merge: %+v", item)
			}
		case "native-last":
			if item.Kind != "course_observation" || item.Status != "unknown" {
				t.Fatalf("last visited was promoted to completion: %+v", item)
			}
		case "native-discussion":
			if item.Kind != "discussion" || item.TaskType != "" || len(item.RecordIDs) != 2 {
				t.Fatalf("legacy exam6 failed reconciliation: %+v", item)
			}
		}
	}
	again, err := buildAcademicProjection("owner", "connection", "mooc", rows)
	must(t, err)
	if !reflect.DeepEqual(p, again) {
		t.Fatal("projection is not deterministic")
	}
	foreign, err := buildAcademicProjection("other", "connection", "mooc", rows)
	must(t, err)
	if foreign.offerings[0].ID == p.offerings[0].ID || foreign.items[0].ID == p.items[0].ID {
		t.Fatal("stable ID omitted owner")
	}
}
func TestAcademicProjectionConservativeTimesAndClasses(t *testing.T) {
	a, b := academicRef("chaoxing", "course", "class-a", ""), academicRef("chaoxing", "course", "class-b", "")
	rows := []model.PlatformRecord{
		academicFixture(1, "chaoxing", "course", a, "", "Same name", map[string]any{"course_id": "course", "class_id": "class-a"}),
		academicFixture(2, "chaoxing", "course", b, "", "Same name", map[string]any{"course_id": "course", "class_id": "class-b"}),
		academicFixture(3, "chaoxing", "discussion", "topic", a, "Topic A", map[string]any{"course_id": "course", "class_id": "class-a", "createTime": "11-04 15:30", "published_at_source": "createTime"}),
		academicFixture(4, "chaoxing", "discussion", "topic", b, "Topic B", map[string]any{"course_id": "course", "class_id": "class-b", "createTime": 1800000000123, "updateTime": "2026-11-04 18:05"}),
		academicFixture(5, "chaoxing", "notice", "notice", "", "Same name", map[string]any{"sendTime": 1800000000123, "insertTime": 1700000000000, "author": "Teacher"}),
		academicFixture(6, "chaoxing", "exam", "exam", "account-exam-schedule", "Same name", map[string]any{"time_raw": "2026-11-04 10:00 ~ 2026-11-04 12:00"}),
		academicFixture(7, "chaoxing", "material", "file", a, "File", map[string]any{"course_id": "course", "class_id": "class-a", "time_raw": "2026-11-04", "parent_data_id": "folder-parent", "source_inconsistent": true, "time_precision": "day", "time_semantics": "unknown", "time_reason": "source does not identify created or updated"}),
		academicFixture(8, "chaoxing", "assignment", "task", a, "Task", map[string]any{"course_id": "course", "class_id": "class-a", "deadline_raw": "剩余3天", "submitEndTime": 1800000000123, "status": "待重做"}),
	}
	p, err := buildAcademicProjection("owner", "connection", "chaoxing", rows)
	must(t, err)
	if len(p.offerings) != 2 || p.offerings[0].CourseID != p.offerings[1].CourseID {
		t.Fatal("catalog course should have two separate offerings")
	}
	var topics []model.AcademicItem
	for _, item := range p.items {
		switch item.NativeID {
		case "topic":
			topics = append(topics, item)
			if item.CourseRef == a && (item.Published.Value != nil || item.Published.State != "partial" || item.Published.Precision != "month_day_minute") {
				t.Fatal("yearless publication guessed a year")
			}
		case "notice":
			if item.OfferingID != "" || item.Published.SourceField != "sendTime" || item.Author != "Teacher" {
				t.Fatal("notice inferred course by title or lost timestamp/author")
			}
		case "exam":
			if item.OfferingID != "" || item.Start.Value == nil || item.End.Value == nil || item.Published.Value != nil {
				t.Fatal("exam range became publication or inferred course")
			}
		case "file":
			metadata := map[string]any{}
			must(t, json.Unmarshal(item.Metadata, &metadata))
			if metadata["parent_data_id"] != "folder-parent" || metadata["source_inconsistent"] != true || metadata["time_precision"] != "day" || metadata["time_semantics"] != "unknown" || metadata["time_reason"] != "source does not identify created or updated" {
				t.Fatal("material hierarchy or source uncertainty metadata lost")
			}
			if item.Published.Value != nil || item.Updated.Value != nil || !strings.Contains(string(item.Metadata), "unclassified_time") {
				t.Fatal("material's ambiguous time semantics coerced")
			}
		case "task":
			if item.Deadline.Value == nil || item.Deadline.SourceField != "submitEndTime" || item.Status != "needs_redo" {
				t.Fatal("absolute deadline or source status lost")
			}
		}
	}
	if len(topics) != 2 || topics[0].ID == topics[1].ID || topics[0].OfferingID == topics[1].OfferingID {
		t.Fatal("teaching classes mixed")
	}
}
func TestAcademicMOOCNativeKeyEvolutionPreservesScope(t *testing.T) {
	encode := func(parts ...string) string { return base64.RawURLEncoding.EncodeToString(academicJSON(parts)) }
	ref := academicRef("mooc", "course", "", "owner-term")
	rows := []model.PlatformRecord{
		academicFixture(1, "mooc", "exam", encode("course", "owner-term", "unit", "content", "node"), ref, "Old discussion", map[string]any{"courseId": "course", "termId": "owner-term", "contentType": 6, "task_type": "exam"}),
		academicFixture(2, "mooc", "discussion", encode("course", "owner-term", "owner-term", "unit", "content", "node"), ref, "New discussion", map[string]any{"courseId": "course", "termId": "owner-term", "sourceTermId": "owner-term", "contentType": 6}),
		academicFixture(3, "mooc", "discussion", encode("course", "owner-term", "reference-term", "unit", "content", "node"), ref, "Reference discussion", map[string]any{"courseId": "course", "termId": "owner-term", "sourceTermId": "reference-term", "contentType": 6}),
	}
	p, err := buildAcademicProjection("owner", "connection", "mooc", rows)
	must(t, err)
	if len(p.items) != 2 {
		t.Fatalf("key evolution must merge owner term but isolate source reference: %d", len(p.items))
	}
	for _, item := range p.items {
		if item.SourceTermID == "owner-term" && len(item.RecordIDs) != 2 {
			t.Fatal("legacy owner-term discussion was not deduplicated")
		}
	}
}
func TestAcademicCoverageLogicalCountsAndLastVisitDedup(t *testing.T) {
	encode := func(parts ...string) string { return base64.RawURLEncoding.EncodeToString(academicJSON(parts)) }
	ref := academicRef("mooc", "course", "", "term")
	fields := func(extra map[string]any) map[string]any {
		f := map[string]any{"courseId": "course", "termId": "term"}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	rows := []model.PlatformRecord{
		academicFixture(1, "mooc", "course", ref, "", "Course", fields(nil)),
		academicFixture(2, "mooc", "exam", "old-discussion", ref, "Discussion", fields(map[string]any{"contentType": 6, "task_type": "exam"})),
		academicFixture(3, "mooc", "exam", "real-exam", ref, "Exam", fields(map[string]any{"contentType": 4, "source_collection": "exams"})),
		academicFixture(4, "mooc", "progress", encode("course", "term", "lastLearnUnitId"), ref, "", fields(map[string]any{"lastLearnUnitId": "old-unit"})),
		academicFixture(5, "mooc", "progress", encode("course", "term", "term", "lastLearnUnitId"), ref, "", fields(map[string]any{"sourceTermId": "term", "lastLearnUnitId": "new-unit"})),
	}
	p, err := buildAcademicProjection("owner", "connection", "mooc", rows)
	must(t, err)
	if len(p.items) != 3 {
		t.Fatalf("last visit duplicated: %d", len(p.items))
	}
	for _, item := range p.items {
		if item.Kind == "course_observation" {
			if len(item.RecordIDs) != 2 || !strings.Contains(string(item.PersonalState), "new-unit") {
				t.Fatal("last visit dedup lost evidence/latest state")
			}
		}
	}
	var coverage struct {
		Resources map[string]struct {
			ItemCount     int  `json:"item_count"`
			EvidenceCount int  `json:"evidence_record_count"`
			RecordCount   int  `json:"record_count"`
			Complete      bool `json:"complete"`
		} `json:"resources"`
	}
	must(t, json.Unmarshal(p.offerings[0].Coverage, &coverage))
	if coverage.Resources["exam"].ItemCount != 1 || coverage.Resources["exam"].EvidenceCount != 2 || coverage.Resources["exam"].RecordCount != 2 || coverage.Resources["discussion"].ItemCount != 1 || coverage.Resources["discussion"].EvidenceCount != 0 || coverage.Resources["course_observation"].ItemCount != 1 || coverage.Resources["progress"].EvidenceCount != 2 {
		t.Fatalf("logical/evidence counts mixed: %+v", coverage)
	}
	for _, resource := range coverage.Resources {
		if resource.Complete {
			t.Fatal("coverage invented upstream completeness")
		}
	}
}
func TestAcademicMOOCReadableCatalogAndProgress(t *testing.T) {
	for _, progressFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(progressFirst), func(t *testing.T) {
			ref := academicRef("mooc", "course", "", "owner-term")
			common := func(extra map[string]any) map[string]any {
				f := map[string]any{"courseId": "course", "termId": "owner-term", "sourceTermId": "source-term", "sourceCourseId": "source-course", "source_scope": "active_source", "path": []string{"11", "22", "33"}, "path_titles": []string{"第一章", "第一节", "课程资料"}, "chapterId": "11", "lessonId": "22", "unitId": "33"}
				for k, v := range extra {
					f[k] = v
				}
				return f
			}
			course := academicFixture(1, "mooc", "course", ref, "", "Course", common(map[string]any{"material_body_coverage": "unsupported: body not verified", "discussion_body_coverage": "unsupported: body not verified"}))
			material := academicFixture(2, "mooc", "material", "unit-material", ref, "课程资料", common(map[string]any{"contentType": 3, "releaseTime": 1800000000123, "gmtCreate": 1700000000000, "gmtModified": 1800000001123, "unit_type": "document", "body_status": "unsupported", "body_reason": "safe standalone body operation not verified", "time_reason": "only source metadata observed"}))
			progress := academicFixture(3, "mooc", "progress", "unit-material", ref, "课程资料", common(map[string]any{"contentType": 3, "completePercent": 50, "learnTime": 120}))
			if progressFirst {
				material.ObservedAt, progress.ObservedAt = progress.ObservedAt, material.ObservedAt
			}
			unknown := academicFixture(4, "mooc", "material", "unknown-assessment", ref, "待核实测验", common(map[string]any{"contentType": 5, "unit_type": "assessment_unknown", "classification_status": "unsupported_assessment_subtype", "gmtCreate": 1700000000000, "material_body_coverage": "unsupported", "discussion_body_coverage": "unsupported"}))
			firstPublish := academicFixture(5, "mooc", "notice", "notice", ref, "Notice", common(map[string]any{"firstPublishTime": 1800000000123}))
			p, err := buildAcademicProjection("owner", "connection", "mooc", []model.PlatformRecord{course, material, progress, unknown, firstPublish})
			must(t, err)
			if len(p.items) != 3 || len(p.offerings) != 1 {
				t.Fatal("progress duplicated catalog material")
			}
			if !strings.Contains(string(p.offerings[0].SourceTerms), "source-course") || !strings.Contains(string(p.offerings[0].Coverage), "material_body_coverage") || !strings.Contains(string(p.offerings[0].Coverage), "discussion_body_coverage") {
				t.Fatal("source relation or coverage lost")
			}
			for _, item := range p.items {
				if !reflect.DeepEqual(item.Path, []string{"第一章", "第一节", "课程资料"}) {
					t.Fatalf("path did not prefer readable titles: %+v", item.Path)
				}
				if item.ChapterID != "11" || item.LessonID != "22" || item.UnitID != "33" || !strings.Contains(string(item.Metadata), `"path_ids":["11","22","33"]`) {
					t.Fatal("native hierarchy lost")
				}
				switch item.NativeID {
				case "unit-material":
					if item.Kind != "material" || len(item.RecordIDs) != 2 || item.Published.SourceField != "releaseTime" || item.Published.Value == nil || item.Updated.SourceField != "gmtModified" || item.Updated.Value == nil || item.Status != "unknown" || !strings.Contains(string(item.PersonalState), "completePercent") || !strings.Contains(string(item.Metadata), "time_reason") {
						t.Fatalf("material catalog/progress semantics lost: %+v", item)
					}
				case "unknown-assessment":
					if item.Kind != "material" || item.Published.Value != nil || !strings.Contains(string(item.Metadata), "assessment_unknown") || !strings.Contains(string(item.Missing), "assessment_subtype") || !strings.Contains(string(item.Metadata), "discussion_body_coverage") {
						t.Fatal("unknown assessment hidden or creation became publication")
					}
				case "notice":
					if item.Published.SourceField != "firstPublishTime" || item.Published.Value == nil {
						t.Fatal("first publication timestamp lost")
					}
				}
			}
		})
	}
}
func TestAcademicProgressPreservesCollectedContentAndUpdatesReliableTimes(t *testing.T) {
	ref := academicRef("mooc", "course", "", "term")
	for _, kind := range []string{"assignment", "material"} {
		t.Run(kind, func(t *testing.T) {
			base := map[string]any{"courseId": "course", "termId": "term", "body_status": "read", "body_reason": "verified safe body", "unit_type": "document", "catalog_node_type": "unit", "coverage": "body_read", "path": []string{"native-chapter", "native-unit"}, "path_titles": []string{"Collected chapter", "Collected unit"}, "deadline": 1800000000000, "task_type": "quiz"}
			descriptor := academicFixture(1, "mooc", kind, "unit", ref, "Collected title", base)
			progress := academicFixture(2, "mooc", "progress", "unit", ref, "Stale directory title", map[string]any{"courseId": "course", "termId": "term", "body_status": "unsupported", "body_reason": "directory only", "unit_type": "unknown", "catalog_node_type": "unknown", "coverage": "partial", "path": []string{"wrong"}, "deadline": 1800003600000, "task_type": "quiz", "submitStatus": "submitted", "studentScore": 0})
			p, err := buildAcademicProjection("owner", "connection", "mooc", []model.PlatformRecord{descriptor, progress})
			must(t, err)
			if len(p.items) != 1 {
				t.Fatal("progress duplicated descriptor")
			}
			item := p.items[0]
			metadata := map[string]any{}
			must(t, json.Unmarshal(item.Metadata, &metadata))
			if item.Kind != kind || item.Title != "Collected title" || item.Text != "fixture body" || !reflect.DeepEqual(item.Path, []string{"Collected chapter", "Collected unit"}) || metadata["body_status"] != "read" || metadata["body_reason"] != "verified safe body" || metadata["unit_type"] != "document" || metadata["coverage"] != "body_read" || !reflect.DeepEqual(metadata["path_ids"], []any{"native-chapter", "native-unit"}) {
				t.Fatalf("progress corrupted content authority: %+v %s", item, metadata)
			}
			expected := time.UnixMilli(1800003600000)
			if item.Deadline.Value == nil || !item.Deadline.Value.Equal(expected) || item.Status != "submitted" || len(item.RecordIDs) != 2 {
				t.Fatal("reliable progress deadline/personal state/evidence lost")
			}
		})
	}
}
func TestAcademicNonTaskMissingApplicability(t *testing.T) {
	for _, kind := range []string{"material", "chapter", "lesson", "progress", "course_observation", "discussion"} {
		row := academicFixture(1, "mooc", kind, "node", "", "Node", map[string]any{})
		row.Content = academicJSON(map[string]any{"title": "Node", "fields": map[string]any{}})
		raw, err := decodeAcademicRaw(row)
		must(t, err)
		item := academicItemFromRaw("owner", "connection", raw, nil)
		if item.Deadline.State != "not_applicable" || item.Deadline.Value != nil {
			t.Fatalf("non-task deadline reported missing: %s %+v", kind, item.Deadline)
		}
		if (kind == "chapter" || kind == "lesson") && !strings.Contains(string(item.Missing), `"body":"not_applicable"`) {
			t.Fatal("structural body reported uncollected")
		}
	}
	for _, kind := range []string{"material", "discussion"} {
		raw, err := decodeAcademicRaw(academicFixture(1, "mooc", kind, "explicit", "", "Node", map[string]any{"deadline": 1800000000123}))
		must(t, err)
		if item := academicItemFromRaw("owner", "connection", raw, nil); item.Deadline.State != "known" || item.Deadline.Value == nil {
			t.Fatal("explicit deadline discarded")
		}
	}
	raw, err := decodeAcademicRaw(academicFixture(1, "mooc", "material", "unknown", "", "Assessment", map[string]any{"unit_type": "assessment_unknown"}))
	must(t, err)
	if item := academicItemFromRaw("owner", "connection", raw, nil); item.Deadline.State != "missing" {
		t.Fatal("unknown assessment deadline marked inapplicable")
	}
}
func TestAcademicAuthoritativeEmptyContentAndCourseDetails(t *testing.T) {
	ref := academicRef("mooc", "course", "", "term")
	encode := func(parts ...string) string { return base64.RawURLEncoding.EncodeToString(academicJSON(parts)) }
	course := academicFixture(1, "mooc", "course", ref, "", "Course", map[string]any{"courseId": "course", "termId": "term", "sourceTermId": "active-term", "sourceCourseId": "source-course", "source_scope": "active_source", "requirements": "Read chapters", "teachingGoal": "Understand", "target": "Students", "courseLoad": "2 hours", "courseStyle": "lecture", "courseName": "Native Course", "teachers": []string{"Teacher A", "Teacher B"}, "cookie": "must not escape"})
	old := academicFixture(2, "mooc", "assignment", encode("course", "term", "unit", "content", "node"), ref, "Old", map[string]any{"courseId": "course", "termId": "term", "task_type": "quiz", "body_status": "read", "deadline": 1800000000000})
	current := academicFixture(3, "mooc", "assignment", encode("course", "term", "term", "unit", "content", "node"), ref, "Current", map[string]any{"courseId": "course", "termId": "term", "sourceTermId": "term", "task_type": "quiz", "body_status": "read", "deadline_reason": "not_provided"})
	var currentJSON map[string]any
	must(t, json.Unmarshal(current.Content, &currentJSON))
	currentJSON["content"] = ""
	current.Content = academicJSON(currentJSON)
	p, err := buildAcademicProjection("owner", "connection", "mooc", []model.PlatformRecord{course, old, current})
	must(t, err)
	if len(p.offerings) != 1 || p.offerings[0].Description != "fixture body" || p.offerings[0].Teacher != "Teacher A、Teacher B" || !strings.Contains(string(p.offerings[0].Details), "Read chapters") || strings.Contains(string(p.offerings[0].Details), "cookie") {
		t.Fatal("course description/details lost or leaked")
	}
	var terms []map[string]any
	must(t, json.Unmarshal(p.offerings[0].SourceTerms, &terms))
	if len(terms) != 2 || terms[0]["relation"] != "owner_route" || terms[0]["termId"] != "term" || terms[1]["sourceTermId"] != "active-term" || terms[1]["sourceCourseId"] != "source-course" {
		t.Fatalf("owner route/source reference relations incomplete: %+v", terms)
	}
	if len(p.items) != 1 || p.items[0].Text != "" || p.items[0].Deadline.Value != nil || p.items[0].Deadline.State != "missing" || !strings.Contains(string(p.items[0].Missing), `"body":"source_empty"`) || len(p.items[0].RecordIDs) != 2 {
		t.Fatalf("old evidence resurrected after authoritative empty: %+v", p.items)
	}
	for _, status := range []string{"read", "source_text"} {
		raw := academicRaw{row: model.PlatformRecord{Source: "chaoxing", Kind: "discussion", ExternalID: "id"}, fields: map[string]any{"body_status": status}}
		item := academicItemFromRaw("owner", "connection", raw, nil)
		expected := "source_empty"
		if status == "source_text" {
			expected = "source_text_empty"
		}
		var missing map[string]string
		must(t, json.Unmarshal(item.Missing, &missing))
		if missing["body"] != expected {
			t.Fatal("read-empty misreported", missing)
		}
	}
}
func TestAcademicStatusDoesNotInferAttempts(t *testing.T) {
	for _, f := range []map[string]any{{"usedTryCount": json.Number("5")}, {"submitStatus": json.Number("2")}, {"studentScore": json.Number("100")}, {"lastLearnUnitId": "x"}} {
		status, _ := academicStatus(f)
		if status != "unknown" {
			t.Fatalf("guessed completion: %+v %s", f, status)
		}
	}
	for _, tc := range []struct {
		f    map[string]any
		want string
	}{{map[string]any{"finished": true}, "completed"}, {map[string]any{"submitStatus": "submitted"}, "submitted"}, {map[string]any{"status": "未提交"}, "open"}} {
		status, _ := academicStatus(tc.f)
		if status != tc.want {
			t.Fatal(status, tc.want)
		}
	}
}
func insertAcademicFixture(t *testing.T, db *DB, owner, connection string, row model.PlatformRecord) {
	t.Helper()
	_, err := db.pool.Exec(context.Background(), `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, owner, connection, row.Source, row.Kind, row.ExternalID, row.ParentID, row.Hash, []byte(row.Content), row.ObservedAt)
	must(t, err)
}
func activeAcademicConnection(t *testing.T, db *DB, owner, source string) string {
	t.Helper()
	ctx := context.Background()
	c, err := db.Platforms().Create(ctx, owner, source)
	must(t, err)
	_, err = db.pool.Exec(ctx, `UPDATE platform_connections SET state='active',session='fixture-encrypted' WHERE user_id=$1 AND id=$2`, owner, c.ID)
	must(t, err)
	return c.ID
}
func TestPostgresAcademicRebuildIsolationAndReconciliation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "academic-owner", model.RoleUser)
	v := fixtureUser(t, db, "academic-other", model.RoleUser)
	connection := activeAcademicConnection(t, db, u.ID, "chaoxing")
	foreign := activeAcademicConnection(t, db, v.ID, "chaoxing")
	a, b := academicRef("chaoxing", "c", "a", ""), academicRef("chaoxing", "c", "b", "")
	fixtures := []model.PlatformRecord{
		academicFixture(1, "chaoxing", "course", a, "", "Course A", map[string]any{"course_id": "c", "class_id": "a"}), academicFixture(2, "chaoxing", "course", b, "", "Course B", map[string]any{"course_id": "c", "class_id": "b"}),
		academicFixture(3, "chaoxing", "discussion", "same", a, "Shared words A", map[string]any{"course_id": "c", "class_id": "a", "createTime": "2026-11-04 11:30"}), academicFixture(4, "chaoxing", "discussion", "same", b, "Shared words B", map[string]any{"course_id": "c", "class_id": "b"}),
	}
	for _, row := range fixtures {
		insertAcademicFixture(t, db, u.ID, connection, row)
	}
	insertAcademicFixture(t, db, v.ID, foreign, fixtures[0])
	result, err := db.Academic().Rebuild(ctx, u.ID, "chaoxing")
	must(t, err)
	if result.Connections != 1 || result.Records != 4 || result.Offerings != 2 || result.Items != 2 {
		t.Fatalf("rebuild counts %+v", result)
	}
	tree, err := db.Academic().Tree(ctx, u.ID, "chaoxing", connection)
	must(t, err)
	if len(tree.Offerings) != 2 || len(tree.Items) != 2 || tree.Items[0].OfferingID == tree.Items[1].OfferingID {
		t.Fatal("tree relationship/class isolation")
	}
	for _, offering := range tree.Offerings {
		if offering.Description != "fixture body" || string(offering.Details) != "{}" {
			t.Fatal("v13 offering description/details not persisted")
		}
	}
	again, err := db.Academic().Rebuild(ctx, u.ID, "")
	must(t, err)
	if again.Items != 2 {
		t.Fatal("rebuild duplicated items")
	}
	tree2, err := db.Academic().Tree(ctx, u.ID, "", "")
	must(t, err)
	if !reflect.DeepEqual(tree.Items, tree2.Items) || !reflect.DeepEqual(tree.Offerings, tree2.Offerings) {
		t.Fatal("stable identity/content changed")
	}
	_, err = db.Academic().Tree(ctx, v.ID, "chaoxing", connection)
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatal("foreign tree exposed", err)
	}
	foreignItems, err := db.Academic().Items(ctx, v.ID, model.AcademicQuery{})
	must(t, err)
	if len(foreignItems) != 0 {
		t.Fatal("rebuild leaked owner projection")
	}
	after := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	before := after.Add(time.Hour)
	filtered, err := db.Academic().Items(ctx, u.ID, model.AcademicQuery{CourseRef: a, Kind: "discussion", Search: "Shared", PublishedAfter: &after, PublishedBefore: &before})
	must(t, err)
	if len(filtered) != 1 || filtered[0].CourseRef != a {
		t.Fatal("scope/time/text filter failed")
	}
	// The owner/connection part of the FK cannot be substituted with a foreign connection.
	_, err = db.pool.Exec(ctx, `INSERT INTO academic_item_evidence(user_id,connection_id,item_id,record_id,resource_kind,external_id,parent_id,logical_id) SELECT $1,$2,$3,id,'course','x','','x' FROM platform_records WHERE user_id=$4 LIMIT 1`, u.ID, connection, tree.Items[0].ID, v.ID)
	if err == nil {
		t.Fatal("cross-owner evidence FK accepted")
	}
	// Fail after reconciliation began: all old projection and raw/session must survive.
	_, err = db.pool.Exec(ctx, `CREATE FUNCTION reject_academic() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rollback'; END $$; CREATE TRIGGER reject_academic BEFORE INSERT OR UPDATE ON academic_items FOR EACH ROW EXECUTE FUNCTION reject_academic()`)
	must(t, err)
	_, err = db.Academic().Rebuild(ctx, u.ID, "chaoxing")
	if err == nil {
		t.Fatal("injected rebuild failure succeeded")
	}
	_, err = db.pool.Exec(ctx, `DROP TRIGGER reject_academic ON academic_items; DROP FUNCTION reject_academic()`)
	must(t, err)
	unchanged, err := db.Academic().Tree(ctx, u.ID, "chaoxing", connection)
	must(t, err)
	if !reflect.DeepEqual(tree, unchanged) {
		t.Fatal("failed rebuild partially changed projection")
	}
	// Removing raw evidence explicitly permits pruning that projection, never other owners.
	_, err = db.pool.Exec(ctx, `DELETE FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='discussion' AND parent_id=$3`, u.ID, connection, a)
	must(t, err)
	_, err = db.Academic().Rebuild(ctx, u.ID, "chaoxing")
	must(t, err)
	remaining, err := db.Academic().Items(ctx, u.ID, model.AcademicQuery{})
	must(t, err)
	if len(remaining) != 1 || remaining[0].CourseRef != b {
		t.Fatal("stale reconcile failed")
	}
	var count int
	var session []byte
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM platform_records WHERE user_id=$1 AND connection_id=$2`, u.ID, connection).Scan(&count))
	must(t, db.pool.QueryRow(ctx, `SELECT session FROM platform_connections WHERE user_id=$1 AND id=$2`, u.ID, connection).Scan(&session))
	if count != 3 || string(session) != "fixture-encrypted" {
		t.Fatal("rebuild changed raw records or credentials")
	}
	must(t, db.Platforms().Disconnect(ctx, model.DWSScope{UserID: u.ID, ConnectionID: connection}))
	remaining, err = db.Academic().Items(ctx, u.ID, model.AcademicQuery{})
	must(t, err)
	if len(remaining) != 0 {
		t.Fatal("disabled connection data exposed")
	}
}
func TestPostgresAcademicSyncProjectionRollsBackRawPage(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "academic-page", model.RoleUser)
	connection := activeAcademicConnection(t, db, u.ID, "mooc")
	lease, err := db.Platforms().Acquire(ctx, model.DWSScope{UserID: u.ID, ConnectionID: connection})
	must(t, err)
	ref := academicRef("mooc", "c", "", "t")
	course := academicFixture(1, "mooc", "course", ref, "", "Course", map[string]any{"courseId": "c", "termId": "t"})
	must(t, db.Platforms().Commit(ctx, lease, "course", []model.PlatformRecord{course}, []byte("original-session"), []byte("course-cursor")))
	_, err = db.pool.Exec(ctx, `CREATE FUNCTION reject_academic_page() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rollback'; END $$; CREATE TRIGGER reject_academic_page BEFORE INSERT ON academic_items FOR EACH ROW EXECUTE FUNCTION reject_academic_page()`)
	must(t, err)
	task := academicFixture(2, "mooc", "assignment", "task", ref, "Task", map[string]any{"courseId": "c", "termId": "t", "task_type": "quiz"})
	err = db.Platforms().Commit(ctx, lease, "assignment", []model.PlatformRecord{task}, []byte("bad-session"), []byte("bad-cursor"))
	if err == nil {
		t.Fatal("projection failure committed raw page")
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM platform_records WHERE user_id=$1 AND connection_id=$2`, u.ID, connection).Scan(&count))
	if count != 1 {
		t.Fatal("failed projection retained raw task")
	}
	state, err := db.Platforms().Load(ctx, lease, "assignment")
	must(t, err)
	if len(state.Cursor) != 0 || string(state.Session) != "original-session" {
		t.Fatal("failed projection advanced cursor/session")
	}
	// Tests use the repository's transaction rather than a second external DB path.
	err = db.transaction(ctx, func(tx pgx.Tx) error { return syncAcademicConnection(ctx, tx, u.ID, connection) })
	must(t, err)
}
