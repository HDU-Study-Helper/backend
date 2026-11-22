package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCourseScopePassesRealRuntimeAndCorrection(t *testing.T) {
	svc, repo, store, _, batch := newItemsIntegration(t, "")
	batch.Source = "chaoxing"
	batch.Scope = &model.ControlBatchScope{Kind: "course", ConnectionID: "connection", CourseRef: `["course","class"]`, CourseName: "具体课程"}
	batch.Changes = []model.ControlChange{{ID: "course-change", Source: "chaoxing", Resource: "discussion", After: mustJSON(map[string]any{"title": "同课变化", "content": strings.Repeat("x", 52000)})}}
	a := repo.agents["trigger"]
	a.Limits.ContextBytes = 1 << 20
	repo.agents["trigger"] = a
	calls := 0
	svc.Agents.Runtime.Providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		input := itemsUserInput(t, r)
		var scope model.ControlBatchScope
		if json.Unmarshal(input["scope"], &scope) != nil || scope.CourseRef != batch.Scope.CourseRef || scope.CourseName != "具体课程" {
			t.Fatal("runtime lost course scope")
		}
		var changes []struct {
			Index int             `json:"index"`
			After json.RawMessage `json:"after"`
		}
		if json.Unmarshal(input["changes"], &changes) != nil || len(changes) != 1 || changes[0].Index != 1 || len(changes[0].After) < 52000 || string(input["selection_protocol"]) != `"indices_v1"` {
			t.Fatal("runtime lost index protocol or truncated evidence")
		}
		calls++
		if calls == 1 {
			return itemsFinal(map[string]any{"decision": "bad"}), nil
		}
		if len(input["validation_error"]) == 0 {
			t.Fatal("correction not passed")
		}
		return itemsFinal(map[string]any{"decision": "IGNORE", "reason": "无新增要求", "relevant_indices": []int{}}), nil
	})
	svc.process(context.Background(), batch)
	if calls != 2 || store.batches[batch.ID].Status != "ignored" {
		t.Fatal("course runtime failed", calls, store.batches[batch.ID])
	}
}
