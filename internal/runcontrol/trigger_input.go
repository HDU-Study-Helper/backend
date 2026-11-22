package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"encoding/json"
)

const triggerPageBytes = 128 << 10
const triggerPageEvents = 100

// Leave room for a bounded validation correction without removing evidence.
const triggerCorrectionReserve = 4 << 10

// Non-course batches retain their 48 KiB bound. Only the model-facing
// selection references change; persisted batches keep their real change IDs.
func triggerInput(b model.ControlBatch) (json.RawMessage, error) {
	return encodeTriggerInput(b, nil)
}

func encodeTriggerInput(b model.ControlBatch, page map[string]int) (json.RawMessage, error) {
	changes := []map[string]any{}
	seen := map[string]bool{}
	for i, v := range b.Changes {
		if v.ID == "" || seen[v.ID] {
			return nil, f.Fail("INVALID_TRIGGER_INPUT", "change IDs must be nonempty and unique before numbering")
		}
		seen[v.ID] = true
		// Numbers are local to this model request, including correction requests.
		// The original ordered changes stay host-side for exact ID restoration.
		one := map[string]any{"index": i + 1, "operation": v.Operation, "resource": v.Resource, "source": v.Source, "occurred_at": v.OccurredAt}
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
	// Selection bounds describe only this request's changes, independently of
	// page.index/page.count (which describe batch pagination, not selectors).
	// For an empty page max=0 means that no index is selectable.
	payload := map[string]any{"batch_id": b.ID, "selection_protocol": "indices_v1", "selection_count": len(changes), "selection_bounds": map[string]int{"min": 1, "max": len(changes)}, "changes": changes}
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
			limit = triggerPageBytes
		}
	} else if dwsGroupBatch(b) {
		payload["scope"] = b.Scope
		payload["refresh_id"] = b.RoundID
		payload["processing_unit"] = "dws_group"
		payload["time_semantics"] = "occurred_at表示采集变化观察时间，不是源发布时间；按各条源证据判断新旧"
	}
	if page != nil {
		payload["page"] = page
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

func dwsGroupBatch(b model.ControlBatch) bool {
	return b.Source == "dws" && b.Scope != nil && b.Scope.Kind == "group" && b.Scope.ConnectionID != "" && b.Scope.GroupID != ""
}
