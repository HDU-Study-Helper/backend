package dbbridge

import (
	f "academic/agent/framework"
	"encoding/json"
	"strings"
	"testing"
)

func TestInitialCourseProfileOnlyChecksOptionalMetadataObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"omitted", `{"entity":{"kind":"course"}}`, true},
		{"null", `{"profile_manifest":null}`, true},
		{"empty object", `{"profile_manifest":{}}`, true},
		{"partial metadata", `{"profile_manifest":{"dimensions":{"identity":{"status":"partial"}}}}`, true},
		{"array", `{"profile_manifest":[]}`, false},
		{"string", `{"profile_manifest":"PRIVATE_SOURCE_FACT"}`, false},
		{"number", `{"profile_manifest":42}`, false},
		{"boolean", `{"profile_manifest":true}`, false},
		{"definition array", `[]`, false},
		{"definition null", `null`, false},
		{"invalid JSON", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInitialCourseProfile(f.Workflow{Definition: json.RawMessage(tc.raw)}, nil)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.ok, err)
			}
			if err != nil {
				if f.SafeError(err).Code != "INVALID_ARGUMENT" || strings.Contains(err.Error(), "PRIVATE_SOURCE_FACT") {
					t.Fatalf("unsafe shape diagnostic: %v", err)
				}
			}
		})
	}
}

func TestInitialCourseProfileDoesNotJudgeSurveyCompletion(t *testing.T) {
	// These are metadata, not proof that the model has or has not investigated
	// enough. No seed category, version, dataset or dimension is mandatory here.
	for _, raw := range []string{
		`{"entity":{"kind":"course"},"profile_manifest":{"version":1,"keywords":[],"search_coverage":[{"dataset":"schedules","complete":false}]}}`,
		`{"entity":{"kind":"course"},"profile_manifest":{"version":2,"dimensions":{"identity":{"status":"known","source_refs":[]}}}}`,
		`{"entity":{"kind":"course"},"profile_manifest":{"version":99,"retrieval_strategy":"custom","query_coverage":[],"dimensions":{}}}`,
		`{"entity":{"kind":"course"},"profile_manifest":{"query_coverage":"legacy metadata","dimensions":["historical metadata"]}}`,
	} {
		for _, seeds := range [][]f.Memory{nil, {{Category: "notes", Content: "facts"}}, {{Category: "identity", Content: "facts"}}} {
			if err := validateInitialCourseProfile(f.Workflow{Definition: json.RawMessage(raw)}, seeds); err != nil {
				t.Fatalf("investigation metadata became a business gate: %v", err)
			}
		}
	}
}
