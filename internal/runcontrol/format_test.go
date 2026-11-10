package runcontrol

import (
	"encoding/json"
	"testing"
)

func TestFinalJSONSafeStructuralNormalization(t *testing.T) {
	valid := `{"items":[{"item_id":"root","status":"IGNORED","reason":"旧目录"}]}`
	for _, input := range []string{valid, "```json\n" + valid + "\n```", valid[:len(valid)-2]} {
		got := normalizedResultJSON(input)
		if !json.Valid([]byte(got)) {
			t.Fatal("not fixed", got)
		}
		items, e := parseItemResults(input, "root")
		if e != nil || len(items) != 1 || items[0].Reason != "旧目录" {
			t.Fatal(items, e)
		}
	}
	for _, input := range []string{`{"items":[{"reason":"unfinished`, `{"items":[}`, `{"items":[{"reason":`} {
		if normalizedResultJSON(input) != input {
			t.Fatal("unsafe normalization", input)
		}
	}
}
