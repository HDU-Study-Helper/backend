package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"encoding/json"
)

// Course batches are kept intact. Legacy platform batches keep their prior input
// shape and 48 KiB bound; a course input can use up to 768 KiB without truncation.
// Unassociated connection batches retain the smaller bound.
func triggerInput(b model.ControlBatch) (json.RawMessage, error) {
	changes := []map[string]any{}
	for _, v := range b.Changes {
		one := map[string]any{"change_id": v.ID, "operation": v.Operation, "resource": v.Resource, "source": v.Source, "occurred_at": v.OccurredAt, "evidence_ids": []string{v.ID}}
		if len(v.Before) > 0 {
			one["before"] = v.Before
		}
		if len(v.After) > 0 {
			one["after"] = v.After
		}
		if len(v.ChangedFields) > 0 {
			one["changed_fields"] = v.ChangedFields
		}
		changes = append(changes, one)
	}
	payload := map[string]any{"batch_id": b.ID, "changes": changes}
	limit := 48 << 10
	if courseBatch(b) {
		payload["scope"] = b.Scope
		payload["refresh_id"] = b.RoundID
		payload["processing_unit"] = "course"
		if b.Scope.Kind == "unassociated" {
			payload["processing_unit"] = "unassociated_connection"
		}
		payload["time_semantics"] = "occurred_at表示采集变化观察时间，不是源发布时间；按各条源证据判断新旧"
		if b.Scope.Kind == "course" {
			limit = 768 << 10
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, f.Fail("TRIGGER_INPUT_TOO_LARGE", "batch exceeds trigger input budget; complete evidence remains saved")
	}
	return raw, nil
}
func courseBatch(b model.ControlBatch) bool {
	return (b.Source == "chaoxing" || b.Source == "mooc") && b.Scope != nil
}
