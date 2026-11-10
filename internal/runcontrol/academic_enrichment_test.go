package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

type academicEnrichmentData struct {
	enrichmentData
	academic []f.Query
	rows     []map[string]any
}

func (d *academicEnrichmentData) Catalog(ctx context.Context, s f.Scope) ([]f.Dataset, error) {
	sets, err := d.enrichmentData.Catalog(ctx, s)
	return append(sets, f.Dataset{Name: "academic_items"}), err
}
func (d *academicEnrichmentData) Query(ctx context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	if q.Dataset == "academic_items" {
		d.academic = append(d.academic, q)
		if d.rows != nil {
			return f.QueryResult{Rows: d.rows}, nil
		}
		return f.QueryResult{Rows: []map[string]any{{"id": academicEvidenceID, "connection_id": "connection", "kind": "discussion", "record_ids": "[\"10\"]", "course_ref": "[\"course\",\"class\"]"}}}, nil
	}
	return d.enrichmentData.Query(ctx, s, q)
}
func TestAcademicEnrichmentJoinsExactRawEvidence(t *testing.T) {
	d := &academicEnrichmentData{}
	s := &Service{Data: d, Agents: &agent.Service{Registry: f.Registry{Repository: &enrichmentRepo{}}}}
	change := model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"}
	ctx := s.enrichItemContext(context.Background(), model.ControlBatch{OwnerID: "owner"}, change)
	if knownEvidenceReferences(ctx)[academicEvidenceID] {
		t.Fatal("academic ID leaked into bare shared namespace")
	}
	validator := &Service{}
	if !validator.validateItemEvidence(ctx, "owner", mustJSON([]string{academicEvidenceID}), change) || !validator.validateItemEvidence(ctx, "owner", json.RawMessage(`[{"type":"academic_item_id","id":"`+academicEvidenceID+`"}]`), change) {
		t.Fatal("host structured mapping not trusted")
	}
	if validator.validateItemEvidence(ctx, "other", mustJSON([]string{academicEvidenceID}), change) {
		t.Fatal("host mapping reused by other owner")
	}
	for _, foreign := range []model.ControlChange{{RecordID: "other", ConnectionID: "connection"}, {RecordID: "10", ConnectionID: "other"}} {
		if validator.validateItemEvidence(ctx, "owner", mustJSON([]string{academicEvidenceID}), foreign) {
			t.Fatal("host mapping reused for unrelated change")
		}
	}
	if len(d.academic) != 1 {
		t.Fatal("structured evidence not loaded")
	}
	q := d.academic[0]
	if len(q.Fields) > 32 || len(q.Filters) != 2 || q.Filters[0].Field != "connection_id" || q.Filters[0].Value != "connection" || q.Filters[1].Field != "record_ids" || q.Filters[1].Value != "\"10\"" {
		t.Fatalf("unscoped or ambiguous evidence lookup %#v", q)
	}
}

func TestAcademicEnrichmentDoesNotTrustUnrelatedOrAmbiguousMapping(t *testing.T) {
	valid := map[string]any{"id": academicEvidenceID, "connection_id": "connection", "record_ids": `["10"]`}
	for _, rows := range [][]map[string]any{
		{valid, valid},
		{{"id": academicEvidenceID, "connection_id": "other", "record_ids": `["10"]`}},
		{{"id": academicEvidenceID, "connection_id": "connection", "record_ids": `["110"]`}},
	} {
		d := &academicEnrichmentData{rows: rows}
		s := &Service{Data: d, Agents: &agent.Service{Registry: f.Registry{Repository: &enrichmentRepo{}}}}
		change := model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"}
		ctx := s.enrichItemContext(context.Background(), model.ControlBatch{OwnerID: "owner"}, change)
		if (&Service{}).validateItemEvidence(ctx, "owner", mustJSON([]string{academicEvidenceID}), change) {
			t.Fatal("invalid host mapping trusted")
		}
	}
}
