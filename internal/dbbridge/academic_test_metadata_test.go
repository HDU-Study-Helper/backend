package dbbridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"academic/backend/internal/model"
)

func TestAcademicCatalogTestMetadataPreservesSourceEvidence(t *testing.T) {
	for _, task := range []string{"quiz", "homework"} {
		t.Run(task, func(t *testing.T) {
			ref := academicRef("mooc", "course", "", "term")
			nodeTimes := map[string]any{"releaseTime": json.Number("1790000000123"), "deadline": "2026-12-29 20:00"}
			testTimes := map[string]any{"releaseTime": json.Number("1791000000456"), "deadline": "2026-12-28 18:30", "submitEndTime": "2026-12-29 20:00"}
			testMetadata := map[string]any{
				"examId": "0000123", "isTestChecked": false, "testDraftStatus": json.Number("0"),
				"testTime": json.Number("0"), "trytime": json.Number("0"), "evaluateJudgeType": json.Number("7"),
				"testScore": json.Number("37"), "userScore": nil, "usedTryCount": json.Number("0"),
				"releaseTime": testTimes["releaseTime"], "deadline": testTimes["deadline"], "submitEndTime": testTimes["submitEndTime"],
				"cookie": "PRIVATE", "questions": []any{"PRIVATE"}, "answers": "PRIVATE", "content": "PRIVATE",
				"profile": map[string]any{"name": "PRIVATE"}, "sourceProvenance": map[string]any{"cookie": "PRIVATE"},
				"statusName": strings.Repeat("x", 513), "studentScore": map[string]any{"cookie": "PRIVATE"},
				"finishStatus": []any{false}, "evaluationStatusText": "",
			}
			fields := map[string]any{
				"courseId": "course", "termId": "term", "task_type": task, "unit_type": task,
				"examId": "0000123", "isTestChecked": false, "testDraftStatus": json.Number("0"),
				"testTime": json.Number("0"), "trytime": json.Number("0"), "evaluateJudgeType": json.Number("7"),
				"releaseTime": nodeTimes["releaseTime"], "deadline": nodeTimes["deadline"], "userScore": nil,
				"test_metadata": testMetadata, "assessment_time_fields": map[string]any{"node": nodeTimes, "test": testTimes},
				"cookie": "PRIVATE", "sourceProvenance": map[string]any{"cookie": "PRIVATE"},
			}
			p, err := buildAcademicProjection("owner", "connection", "mooc", []model.PlatformRecord{
				academicFixture(1, "mooc", "assignment", "unit", ref, "Task", fields),
			})
			must(t, err)
			if len(p.items) != 1 {
				t.Fatal("missing catalog assignment", p.items)
			}
			item := p.items[0]
			if item.Status != "unknown" || item.TaskType != task || item.Deadline.Raw != "2026-12-29 20:00" || item.Published.Raw != "1790000000123" {
				t.Fatalf("source enum or independent node time changed: %+v", item)
			}
			var meta, personal map[string]any
			decode := func(raw json.RawMessage, dst *map[string]any) {
				t.Helper()
				d := json.NewDecoder(strings.NewReader(string(raw)))
				d.UseNumber()
				must(t, d.Decode(dst))
			}
			decode(item.Metadata, &meta)
			decode(item.PersonalState, &personal)
			for _, key := range []string{"examId", "isTestChecked", "testDraftStatus", "testTime", "trytime", "evaluateJudgeType"} {
				if !reflect.DeepEqual(meta[key], fields[key]) {
					t.Fatalf("top-level scalar %s changed: %#v", key, meta[key])
				}
			}
			wantTest := map[string]any{}
			for _, key := range []string{"examId", "isTestChecked", "testDraftStatus", "testTime", "trytime", "evaluateJudgeType", "testScore", "usedTryCount", "releaseTime", "deadline", "submitEndTime"} {
				wantTest[key] = testMetadata[key]
			}
			if !reflect.DeepEqual(meta["test_metadata"], wantTest) {
				t.Fatalf("nested test metadata changed or leaked: %#v", meta["test_metadata"])
			}
			if !reflect.DeepEqual(meta["assessment_time_fields"], map[string]any{"node": nodeTimes, "test": testTimes}) {
				t.Fatalf("node and test time evidence conflated: %#v", meta["assessment_time_fields"])
			}
			if value, exists := personal["userScore"]; exists && value != nil {
				t.Fatalf("source-null userScore became a score: %#v", value)
			}
			for _, raw := range []json.RawMessage{item.Metadata, item.PersonalState} {
				if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "sourceProvenance") {
					t.Fatal("unauthorized nested content escaped", string(raw))
				}
			}
		})
	}
}

func TestAcademicTestMetadataRejectsNonScalarAndOversizedValues(t *testing.T) {
	for _, value := range []any{[]any{"PRIVATE"}, nil, "PRIVATE", false, json.Number("0")} {
		got := academicSafeFields(map[string]any{"test_metadata": value}, []string{"test_metadata"})
		if nested, exists := got["test_metadata"]; exists && !reflect.DeepEqual(nested, map[string]any{}) {
			t.Fatalf("invalid test metadata escaped: %#v", got)
		}
	}
	for _, key := range safeTestMetadataKeys {
		for _, value := range []any{map[string]any{"cookie": "PRIVATE"}, []any{"PRIVATE"}, strings.Repeat("x", 513)} {
			got := academicSafeFields(map[string]any{"test_metadata": map[string]any{key: value}}, []string{"test_metadata"})
			if nested := got["test_metadata"].(map[string]any); len(nested) != 0 {
				t.Fatalf("non-scalar or oversized %s escaped: %#v", key, nested)
			}
		}
	}
}

func TestAcademicTestMetadataAgentProjectionIsBoundedAndShared(t *testing.T) {
	projection := safeTestMetadataProjection("fixture")
	for _, guard := range []string{"jsonb_typeof(fixture)='object'", "jsonb_typeof(kv.value) IN ('string','number','boolean')", "octet_length(kv.value #>> '{}') BETWEEN 1 AND 512"} {
		if !strings.Contains(projection, guard) {
			t.Fatalf("missing stored metadata guard %q", guard)
		}
	}
	for _, key := range safeTestMetadataKeys {
		if !strings.Contains(projection, "'"+key+"'") {
			t.Fatalf("source scalar absent from agent projection: %s", key)
		}
	}
	for _, key := range []string{"cookie", "questions", "answers", "content", "profile", "sourceProvenance", "test_metadata"} {
		if strings.Contains(projection, "'"+key+"'") {
			t.Fatalf("unauthorized nested key allowed: %s", key)
		}
	}
	for _, projection := range []string{dataFields["metadata"], academicDatasets()["academic_items"].fields["metadata"]} {
		if !strings.Contains(projection, "jsonb_build_object('test_metadata',") || !strings.Contains(projection, "octet_length(kv.value #>> '{}') BETWEEN 1 AND 512") {
			t.Fatal("source and typed academic metadata must share the bounded test projection")
		}
		for _, key := range []string{"examId", "isTestChecked", "testDraftStatus", "testTime", "trytime", "evaluateJudgeType"} {
			if !strings.Contains(projection, "->'"+key+"'") {
				t.Fatalf("top-level scalar absent from agent projection: %s", key)
			}
		}
	}
}
