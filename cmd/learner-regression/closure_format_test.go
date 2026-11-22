package main

import "testing"

func TestClosureLeadingJSONFieldsDoNotRepairMalformedTail(t *testing.T) {
	raw := `{"session_id":"literal-$ID","completion":{"marker":"READY_TO_COMMIT","staging_revision":4},"limitations":["a"}],"other":null}`
	value, ok := closureLeadingJSONField(raw, "session_id")
	if !ok || value != "literal-$ID" {
		t.Fatal(value, ok)
	}
	value, ok = closureLeadingJSONField(raw, "completion")
	if !ok || value.(map[string]any)["staging_revision"] != float64(4) {
		t.Fatal(value, ok)
	}
	if _, ok = closureLeadingJSONField(raw, "other"); ok {
		t.Fatal("malformed tail was silently repaired")
	}
}
