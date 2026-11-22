package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	f "academic/agent/framework"
)

func TestHostCatalogAllContractsAndFieldDescriptions(t *testing.T) {
	specs, err := hostTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 15 {
		t.Fatal(len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		if names[s.Name] {
			t.Fatal("duplicate", s.Name)
		}
		names[s.Name] = true
		for _, raw := range []json.RawMessage{s.InputSchema, s.OutputSchema} {
			var schema map[string]any
			if json.Unmarshal(raw, &schema) != nil {
				t.Fatal("invalid schema", s.Name)
			}
			requireDescriptions(t, s.Name, schema)
		}
	}
	for _, n := range []string{"schedule_query", "schedule_get", "schedule_create", "schedule_update", "schedule_delete", "learner_stage_state", "learner_stage_select", "public_todo_upsert", "public_todo_list", "public_todo_complete", "notice_create", "notice_list", "planned_job_create", "planned_job_list", "planned_job_cancel"} {
		if !names[n] {
			t.Fatal("missing", n)
		}
	}
	for _, s := range f.ModelFacingTools(specs) {
		if !strings.Contains(s.Description, "Return contract") {
			t.Fatal("missing output projection", s.Name)
		}
	}
}
func requireDescriptions(t *testing.T, path string, node map[string]any) {
	t.Helper()
	if props, ok := node["properties"].(map[string]any); ok {
		for name, v := range props {
			child := v.(map[string]any)
			if text, ok := child["description"].(string); !ok || strings.TrimSpace(text) == "" {
				t.Error("missing field description", path+"."+name)
			}
			requireDescriptions(t, path+"."+name, child)
		}
	}
	if item, ok := node["items"].(map[string]any); ok {
		requireDescriptions(t, path+"[]", item)
	}
}
func TestHostCatalogGeneratedSnapshotMatches(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "tool-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	actual, err := hostTools()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(actual)
	var normalized any
	_ = json.Unmarshal(a, &normalized)
	a, _ = json.Marshal(normalized)
	b, _ := json.Marshal(saved)
	if string(a) != string(b) {
		t.Fatal("host tools changed; regenerate with go run ./cmd/tool-catalog > internal/agent/tool-catalog.json")
	}
}
