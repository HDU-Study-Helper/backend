package mooc

import (
	"academic/backend/internal/contract"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Opt-in offline replay; no credentials are loaded and no upstream is contacted.
func TestMOOCCapturedOriginalCatalogReplay(t *testing.T) {
	p := os.Getenv("MOOC_CAPTURED_CATALOG")
	if p == "" {
		t.Skip("optional locally captured original response")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal("capture unavailable")
	}
	var env map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.UseNumber()
	if decoder.Decode(&env) != nil {
		t.Fatal("invalid capture")
	}
	result := object(env["result"])
	dto := object(result["mocTermDto"])
	if dto == nil {
		t.Fatal("missing original catalog")
	}
	co := course{ID: nativeID(dto, "courseId"), Term: nativeID(dto, "id"), Name: "Captured course"}
	rows, e := termRecords(result, co, contract.Discussion)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 24 {
		t.Fatalf("expected verified 24 discussion units, got %d", len(rows))
	}
	green, gray := 0, 0
	for _, r := range rows {
		switch text(r.Fields["viewStatus"]) {
		case "5":
			green++
			if r.Fields["task_point_completed"] != true || r.Fields["participation_status"] != "participated" || r.Fields["current_user_reply_count"] != int64(1) {
				t.Fatal("completed personal state lost")
			}
		case "0":
			gray++
			if r.Fields["task_point_completed"] != false || r.Fields["participation_status"] != "unknown" {
				t.Fatal("incomplete/unknown changed")
			}
			if v, ok := r.Fields["replyCount"]; !ok || v != nil {
				t.Fatal("explicit source null lost")
			}
		default:
			t.Fatal("unexpected source status")
		}
	}
	if green != 11 || gray != 13 {
		t.Fatal("source evidence count changed")
	}
	tasks, e := termRecords(result, co, contract.Assignment)
	if e != nil || len(tasks) != 10 {
		t.Fatal("assessment evidence changed", e, len(tasks))
	}
	for _, r := range tasks {
		if text(r.Fields["deadline"]) == "" {
			t.Fatal("source deadline lost")
		}
	}
	t.Logf("Original catalog replay: %d discussions, %d green/positive personal counts, %d grey/null, %d assessments", len(rows), green, gray, len(tasks))
}
