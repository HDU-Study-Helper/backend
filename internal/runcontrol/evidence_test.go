package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const academicEvidenceID = "857081b050ef3fd5d09c33cbd97fa538de4c5a939fdfc9244338a18d74899ddd"

type academicEvidenceData struct {
	f.DataService
	rows  []map[string]any
	calls int
}

func (d *academicEvidenceData) Query(_ context.Context, scope f.Scope, q f.Query) (f.QueryResult, error) {
	d.calls++
	if scope.OwnerID != "owner" {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "owner or inactive connection")
	}
	if q.Dataset != "academic_items" || strings.Join(q.Fields, ",") != "id,connection_id,record_ids" || q.Limit != 2 || len(q.Filters) != 2 || q.Filters[0].Field != "connection_id" || q.Filters[0].Operator != "eq" || q.Filters[0].Value != "connection" || q.Filters[1].Field != "id" || q.Filters[1].Operator != "eq" {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "unscoped query")
	}
	return f.QueryResult{Rows: d.rows}, nil
}

func TestAcademicEvidenceExactQueryAndReferenceForms(t *testing.T) {
	for _, records := range []any{`["56126","other"]`, []string{"56126"}, []any{"56126"}} {
		for _, ref := range []any{academicEvidenceID, "academic_items.id=" + academicEvidenceID, "academic_items.id:" + academicEvidenceID, "academic_item_id=" + academicEvidenceID, "academic_item_id:" + academicEvidenceID, map[string]string{"type": "academic_item_id", "id": academicEvidenceID}, map[string]string{"kind": "academic_item", "value": academicEvidenceID}, map[string]string{"type": "academic_items.id", "academic_item_id": academicEvidenceID}, map[string]string{"academic_item_id": academicEvidenceID}} {
			d := &academicEvidenceData{rows: []map[string]any{{"id": academicEvidenceID, "connection_id": "connection", "record_ids": records}}}
			s := &Service{Data: d}
			if !s.validateItemEvidence(context.Background(), "owner", mustJSON([]any{ref}), model.ControlChange{RecordID: "56126", ConnectionID: "connection"}) || d.calls != 1 {
				t.Fatalf("exact structured evidence rejected: %#v records=%#v calls=%d", ref, records, d.calls)
			}
		}
	}
}

func TestAcademicEvidenceKeepsNativeExternalIDCompatibility(t *testing.T) {
	change := model.ControlChange{RecordID: "56126", ConnectionID: "connection", After: mustJSON(map[string]string{"external_id": academicEvidenceID})}
	s := &Service{}
	if !s.validateItemEvidence(context.Background(), "owner", mustJSON([]string{academicEvidenceID}), change) {
		t.Fatal("original native external ID rejected")
	}
	if s.validateItemEvidence(context.Background(), "owner", mustJSON([]any{map[string]string{"type": "academic_item_id", "id": academicEvidenceID}}), change) {
		t.Fatal("typed academic ID fell back to native external ID")
	}
}

func TestAcademicEvidenceRejectsWrongScopeAndNamespaceFallback(t *testing.T) {
	change := model.ControlChange{ID: "change", RecordID: "56126", ConnectionID: "connection"}
	for _, tc := range []struct {
		name, owner string
		row         map[string]any
		ref         any
		duplicate   bool
	}{
		{name: "wrong owner", owner: "other"},
		{name: "wrong connection", row: map[string]any{"connection_id": "other"}},
		{name: "unrelated record", row: map[string]any{"record_ids": `["56127"]`}},
		{name: "record substring", row: map[string]any{"record_ids": `["156126"]`}},
		{name: "malformed record ids", row: map[string]any{"record_ids": `56126`}},
		{name: "wrong item", row: map[string]any{"id": strings.Repeat("a", 64)}},
		{name: "ambiguous rows", duplicate: true},
		{name: "raw record fallback", ref: map[string]string{"type": "academic_item_id", "record_id": "56126"}},
		{name: "raw id collision", ref: map[string]string{"type": "academic_item_id", "id": "56126"}},
		{name: "known course collision", ref: map[string]string{"type": "academic_item_id", "id": academicEvidenceID}, row: map[string]any{"record_ids": `["unrelated"]`}},
		{name: "arbitrary name", ref: "academic_items.id=made-up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"id": academicEvidenceID, "connection_id": "connection", "record_ids": `["56126"]`}
			for k, v := range tc.row {
				row[k] = v
			}
			d := &academicEvidenceData{rows: []map[string]any{row}}
			if tc.duplicate {
				d.rows = append(d.rows, row)
			}
			owner := tc.owner
			if owner == "" {
				owner = "owner"
			}
			ref := tc.ref
			if ref == nil {
				ref = map[string]string{"type": "academic_item_id", "id": academicEvidenceID}
			}
			ctx := context.WithValue(context.Background(), knownEvidenceKey{}, map[string]bool{academicEvidenceID: true, "56126": true})
			if (&Service{Data: d}).validateItemEvidence(ctx, owner, mustJSON([]any{ref}), change) {
				t.Fatalf("invalid academic ref accepted: %#v", ref)
			}
		})
	}
}

type evidenceData struct {
	f.DataService
	owner, connection string
	calls             int
}

func (d *evidenceData) Query(_ context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	d.calls++
	if s.OwnerID != d.owner || len(q.Filters) != 2 || q.Filters[0].Field != "connection_id" || q.Filters[0].Value != d.connection {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "scope")
	}
	if q.Filters[1].Value == "related-course" {
		return f.QueryResult{Rows: []map[string]any{{"id": "50200", "external_id": "related-course", "connection_id": d.connection}}}, nil
	}
	return f.QueryResult{Rows: []map[string]any{}}, nil
}
func TestCurrentBatchEventEvidenceEvenWithoutDataProjection(t *testing.T) {
	s := &Service{}
	change := model.ControlChange{ID: "change", Source: "dws"}
	ctx := s.enrichItemContext(context.Background(), model.ControlBatch{ID: "batch"}, change)
	if !s.validateItemEvidence(ctx, "owner", json.RawMessage(`["batch",{"kind":"event_id","event_id":"batch"}]`), change) {
		t.Fatal("host event reference rejected")
	}
	if s.validateItemEvidence(ctx, "owner", json.RawMessage(`["another-batch"]`), change) {
		t.Fatal("foreign event reference accepted")
	}
}
func TestRelatedEvidenceWithinOwnerConnection(t *testing.T) {
	data := &evidenceData{owner: "owner", connection: "connection"}
	s := &Service{Data: data}
	change := model.ControlChange{ID: "change", RecordID: "48413", ConnectionID: "connection"}
	ctx := context.WithValue(context.Background(), knownEvidenceKey{}, map[string]bool{"50200": true})
	if !s.validateItemEvidence(ctx, "owner", json.RawMessage(`["change","connection","50200","related-course","record_id:50200","connection_id:connection","change_id:change","platform_records.id=48413","change_id=change","connection_id=connection","external_id=related-course"]`), change) {
		t.Fatal("verified related course rejected")
	}
	if data.calls == 0 {
		t.Fatal("additional ref not queried")
	}
	if s.validateItemEvidence(ctx, "owner", json.RawMessage(`[{"kind":"change_id","value":"50200"}]`), change) {
		t.Fatal("unknown change id accepted")
	}
	if s.validateItemEvidence(ctx, "owner", json.RawMessage(`["change_id:50200"]`), change) {
		t.Fatal("prefixed unknown change accepted")
	}
	if s.validateItemEvidence(ctx, "owner", json.RawMessage(`["made-up"]`), change) {
		t.Fatal("fabricated ref accepted")
	}
	if s.validateItemEvidence(context.Background(), "other", json.RawMessage(`["related-course"]`), change) {
		t.Fatal("other owner ref accepted")
	}
}
