package runcontrol

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

const assignmentNoticeCandidateBytes = 8 << 10

// AssignmentNoticeCandidates are bounded discovery evidence, not a confirmed
// course association or a derived deadline/student-status assertion.
type AssignmentNoticeCandidates struct {
	MatchingBasis     string           `json:"matching_basis"`
	AssociationStatus string           `json:"association_status"`
	Count             int              `json:"count"`
	HasMore           bool             `json:"has_more"`
	Rows              []map[string]any `json:"rows"`
}

// FindAssignmentNoticeCandidates discovers same-owner/account notices whose
// titles contain an already verified assignment title. Account notices commonly
// lack course/class fields, so those fields must not be added to this lookup.
// The caller must still assess the returned text, entity and dates as evidence.
func FindAssignmentNoticeCandidates(ctx context.Context, data f.DataService, scope f.Scope, change model.ControlChange, record, item map[string]any) (*AssignmentNoticeCandidates, error) {
	if data == nil || scope.OwnerID == "" || change.ConnectionID == "" || change.RecordID == "" || change.Source == "" {
		return nil, nil
	}
	if record["id"] != change.RecordID || record["connection_id"] != change.ConnectionID || record["source"] != change.Source {
		return nil, nil
	}
	kind, _ := item["kind"].(string)
	if (kind != "assignment" && kind != "exam") || record["kind"] != kind || !academicItemMatchesChange(item, change) {
		return nil, nil
	}
	if value, ok := item["deadline"].(string); !ok || strings.TrimSpace(value) != "" {
		return nil, nil
	}
	title, ok := item["title"].(string)
	if !ok || title != record["title"] || utf8.RuneCountInString(strings.TrimSpace(title)) < 8 {
		return nil, nil
	}
	q := f.Query{Dataset: "platform_records", Fields: []string{"id", "connection_id", "source", "kind", "title", "text"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "source", Operator: "eq", Value: change.Source}, {Field: "kind", Operator: "eq", Value: "notice"}, {Field: "title", Operator: "contains", Value: title}}, Limit: 3}
	page, err := data.Query(ctx, scope, q)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, row := range page.Rows {
		id, idOK := row["id"].(string)
		rowTitle, titleOK := row["title"].(string)
		if !idOK || id == "" || !titleOK || row["connection_id"] != change.ConnectionID || row["source"] != change.Source || row["kind"] != "notice" || !strings.Contains(rowTitle, title) {
			continue
		}
		// Copy only the requested business projection even if a DataService returns
		// unrelated fields. Never attach arbitrary nested account/credential data.
		safe := map[string]any{}
		valid := true
		for _, field := range q.Fields {
			value, ok := row[field].(string)
			if !ok {
				valid = false
				break
			}
			safe[field] = value
		}
		if valid {
			rows = append(rows, safe)
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	result := &AssignmentNoticeCandidates{MatchingBasis: "same authorized owner, connection and source; notice title contains the verified assignment/exam title", AssociationStatus: "candidate_only_verify_course_and_body_before_use", Count: len(rows), HasMore: page.HasMore, Rows: rows}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > assignmentNoticeCandidateBytes {
		return nil, nil
	}
	return result, nil
}

func marshalEvidenceWithOptionalNotices(facts map[string]any) ([]byte, error) {
	raw, err := json.Marshal(facts)
	if err != nil {
		return nil, err
	}
	if len(raw) > 48<<10 {
		if _, ok := facts["related_account_notice_candidates"]; ok {
			delete(facts, "related_account_notice_candidates")
			return json.Marshal(facts)
		}
	}
	return raw, nil
}
