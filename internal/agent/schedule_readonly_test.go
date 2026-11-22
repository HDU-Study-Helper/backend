package agent

import (
	f "academic/agent/framework"
	"testing"
)

func TestScheduleReadOnlyMetadata(t *testing.T) {
	registry := f.NewToolRegistry()
	if err := RegisterScheduleTools(registry, nil); err != nil {
		t.Fatal(err)
	}
	for _, spec := range registry.Catalog() {
		want := spec.Name == "schedule_query" || spec.Name == "schedule_get"
		if spec.ReadOnly != want {
			t.Fatal("incorrect schedule capability metadata", spec.Name, spec.ReadOnly)
		}
	}
}
