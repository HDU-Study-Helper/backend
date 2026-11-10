package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
)

// validateItemEvidence accepts host-verified course references and verifies other
// related records only inside the same owner's currently authorized connection.
func (s *Service) validateItemEvidence(ctx context.Context, owner string, raw json.RawMessage, change model.ControlChange) bool {
	if len(raw) == 0 {
		return true
	}
	var refs []json.RawMessage
	if json.Unmarshal(raw, &refs) != nil {
		return false
	}
	known := knownEvidenceReferences(ctx)
	for _, ref := range refs {
		var text string
		kind := ""
		if json.Unmarshal(ref, &text) != nil {
			var object struct {
				Kind           string `json:"kind"`
				Type           string `json:"type"`
				Value          string `json:"value"`
				ID             string `json:"id"`
				RecordID       string `json:"record_id"`
				ExternalID     string `json:"external_id"`
				ChangeID       string `json:"change_id"`
				ConnectionID   string `json:"connection_id"`
				EventID        string `json:"event_id"`
				AcademicItemID string `json:"academic_item_id"`
			}
			if json.Unmarshal(ref, &object) != nil {
				return false
			}
			kind = object.Kind
			if kind == "" {
				kind = object.Type
			}
			// Academic references use their own identity namespace. Never let a
			// record_id or a known course ID satisfy a typed academic reference.
			if isAcademicEvidenceKind(kind) || isAcademicEvidenceKind(object.Type) || object.AcademicItemID != "" {
				text = object.AcademicItemID
				if text == "" {
					text = object.ID
				}
				if text == "" {
					text = object.Value
				}
				if !s.validateAcademicItemEvidence(ctx, owner, text, change) {
					return false
				}
				continue
			}
			text = object.Value
			if text == "" {
				text = object.ID
			}
			if text == "" {
				text = object.RecordID
			}
			if text == "" {
				text = object.ExternalID
			}
			if kind == "change_id" && object.ChangeID != "" {
				text = object.ChangeID
			}
			if kind == "connection_id" && object.ConnectionID != "" {
				text = object.ConnectionID
			}
			if kind == "event_id" && object.EventID != "" {
				text = object.EventID
			}
		}
		prefixes := []struct{ Prefix, Kind string }{{"academic_items.id=", "academic_item_id"}, {"academic_item_id=", "academic_item_id"}, {"academic_items.id:", "academic_item_id"}, {"academic_item_id:", "academic_item_id"}, {"platform_records.id=", "record_id"}, {"record_id=", "record_id"}, {"external_id=", "external_id"}, {"connection_id=", "connection_id"}, {"change_id=", "change_id"}, {"record_id:", "record_id"}, {"external_id:", "external_id"}, {"connection_id:", "connection_id"}, {"change_id:", "change_id"}}
		for _, prefix := range prefixes {
			if strings.HasPrefix(text, prefix.Prefix) {
				kind = prefix.Kind
				text = strings.TrimPrefix(text, prefix.Prefix)
				break
			}
		}
		if text == "" {
			return false
		}
		if isAcademicEvidenceKind(kind) {
			if !s.validateAcademicItemEvidence(ctx, owner, text, change) {
				return false
			}
			continue
		}
		if kind == "change_id" {
			if text == change.ID {
				continue
			}
			return false
		}
		if kind == "connection_id" {
			if text == change.ConnectionID {
				continue
			}
			return false
		}
		if validEvidence(mustJSON([]string{text}), change) {
			continue
		}
		if kind == "" && isAcademicItemID(text) {
			if !s.validateAcademicItemEvidence(ctx, owner, text, change) {
				return false
			}
			continue
		}
		if known[text] {
			continue
		}
		if s.Data == nil || change.ConnectionID == "" {
			return false
		}
		found := false
		fields := []string{"id", "external_id"}
		if kind == "record_id" || kind == "course_record_id" {
			fields = []string{"id"}
		}
		if kind == "external_id" {
			fields = []string{"external_id"}
		}
		dataset := "platform_records"
		projection := []string{"id", "external_id", "connection_id"}
		filters := []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}}
		if change.Source == "dws" {
			group := dwsMessageGroup(change)
			if change.Resource != "message" || group == "" {
				return false
			}
			dataset = "dws_messages"
			projection = append(projection, "group_id")
			filters = append(filters, f.Filter{Field: "group_id", Operator: "eq", Value: group})
		}
		for _, field := range fields {
			page, e := s.Data.Query(ctx, f.Scope{OwnerID: owner}, f.Query{Dataset: dataset, Fields: projection, Filters: append(append([]f.Filter{}, filters...), f.Filter{Field: field, Operator: "eq", Value: text}), Limit: 1})
			if e != nil {
				return false
			}
			for _, row := range page.Rows {
				if row["connection_id"] == change.ConnectionID && (row["id"] == text || row["external_id"] == text) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func isAcademicEvidenceKind(kind string) bool {
	return kind == "academic_item_id" || kind == "academic_item" || kind == "academic_items.id"
}

func isAcademicItemID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Service) validateAcademicItemEvidence(ctx context.Context, owner, id string, change model.ControlChange) bool {
	if !isAcademicItemID(id) || owner == "" || change.ConnectionID == "" || change.RecordID == "" {
		return false
	}
	if known := knownAcademicEvidenceReferences(ctx); known.OwnerID == owner && known.ConnectionID == change.ConnectionID && known.RecordID == change.RecordID && known.IDs[id] {
		return true
	}
	if s.Data == nil {
		return false
	}
	page, err := s.Data.Query(ctx, f.Scope{OwnerID: owner}, f.Query{Dataset: "academic_items", Fields: []string{"id", "connection_id", "record_ids"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "id", Operator: "eq", Value: id}}, Limit: 2})
	return err == nil && len(page.Rows) == 1 && page.Rows[0]["id"] == id && academicItemMatchesChange(page.Rows[0], change)
}

// record_ids is a JSON text projection in the authorized data service; accepting
// decoded arrays too keeps host evidence independent of its transport encoding.
func academicItemMatchesChange(row map[string]any, change model.ControlChange) bool {
	if change.RecordID == "" || row["connection_id"] != change.ConnectionID {
		return false
	}
	var ids []string
	switch value := row["record_ids"].(type) {
	case string:
		if json.Unmarshal([]byte(value), &ids) != nil {
			return false
		}
	default:
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &ids) != nil {
			return false
		}
	}
	for _, id := range ids {
		if id == change.RecordID {
			return true
		}
	}
	return false
}
