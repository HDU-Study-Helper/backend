package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMOOCDirectTaskMetadataSQLProjection(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	doc := `{"task_point_completed":false,"task_point_status":"incomplete","current_user_reply_count":null,"native_progress_fields":{"viewStatus":0,"replyCount":null,"lessonLearned":6,"lessonsCount":46},"lessonLearned":6,"lessonsCount":46,"test_metadata":{"usedTryCount":0,"enableEvaluation":false,"userScore":null,"cookie":"PRIVATE","questions":["PRIVATE"]},"cookie":"PRIVATE"}`
	var raw []byte
	query := "SELECT (" + safeMetadataProjection("doc") + ")::jsonb FROM (SELECT $1::jsonb doc)s"
	must(t, db.pool.QueryRow(ctx, query, doc).Scan(&raw))
	var x map[string]any
	must(t, json.Unmarshal(raw, &x))
	if x["task_point_completed"] != false || x["task_point_status"] != "incomplete" || x["lessonLearned"] != float64(6) || x["lessonsCount"] != float64(46) {
		t.Fatal("source scalar lost", x)
	}
	if _, ok := x["current_user_reply_count"]; !ok || x["current_user_reply_count"] != nil {
		t.Fatal("null count lost")
	}
	native := x["native_progress_fields"].(map[string]any)
	if _, ok := native["replyCount"]; !ok || native["replyCount"] != nil {
		t.Fatal("source null lost")
	}
	test := x["test_metadata"].(map[string]any)
	if test["usedTryCount"] != float64(0) || test["enableEvaluation"] != false {
		t.Fatal(test)
	}
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatal("private field escaped")
	}
	if _, ok := test["userScore"]; ok {
		t.Fatal("unknown score must not be synthesized")
	}
	unknown := `{"lessonLearned":null,"lessonsCount":12,"native_progress_fields":{"lessonLearned":null,"lessonsCount":12}}`
	must(t, db.pool.QueryRow(ctx, query, unknown).Scan(&raw))
	must(t, json.Unmarshal(raw, &x))
	if value, present := x["lessonLearned"]; !present || value != nil {
		t.Fatal("unknown learned count became zero or vanished", x)
	}
	nested := x["native_progress_fields"].(map[string]any)
	if value, present := nested["lessonLearned"]; !present || value != nil || nested["lessonsCount"] != float64(12) {
		t.Fatal("unknown counter lost in native scope", nested)
	}
}
