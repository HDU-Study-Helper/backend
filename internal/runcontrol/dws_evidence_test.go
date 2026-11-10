package runcontrol

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

type dwsEvidenceData struct{ queries []f.Query }

func (*dwsEvidenceData) Catalog(context.Context, f.Scope) ([]f.Dataset, error) { return nil, nil }
func (d *dwsEvidenceData) Query(_ context.Context, scope f.Scope, q f.Query) (f.QueryResult, error) {
	d.queries = append(d.queries, q)
	rows := []map[string]any{
		{"id": "71", "external_id": "native-message", "connection_id": "connection", "group_id": "group", "source": "dws", "text": "Notice"},
		{"id": "72", "external_id": "related", "connection_id": "connection", "group_id": "group", "source": "dws"},
		{"id": "73", "external_id": "foreign-group", "connection_id": "connection", "group_id": "other", "source": "dws"},
	}
	out := f.QueryResult{Rows: []map[string]any{}}
	if q.Dataset != "dws_messages" || scope.OwnerID != "owner" {
		return out, nil
	}
	for _, row := range rows {
		match := true
		for _, filter := range q.Filters {
			if row[filter.Field] != filter.Value {
				match = false
			}
		}
		if match {
			out.Rows = append(out.Rows, row)
		}
	}
	return out, nil
}
func TestDWSHistoricMessageEvidenceUsesNativeIDAndGroup(t *testing.T) {
	change := model.ControlChange{ID: "change", Source: "dws", Resource: "message", RecordID: "native-message", ConnectionID: "connection", After: json.RawMessage(`{"messageId":"native-message","conversationId":"group","text":"Notice"}`)}
	batch := model.ControlBatch{ID: "batch", OwnerID: "owner", Source: "dws"}
	raw, err := filterInput(batch, []model.ControlChange{change})
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Links []filterEvidence  `json:"evidence_links"`
		Query map[string]string `json:"evidence_query"`
	}
	if json.Unmarshal(raw, &input) != nil || len(input.Links) != 1 || input.Links[0].ExternalID != "native-message" || input.Links[0].GroupID != "group" || input.Query["dataset"] != "dws_messages" || input.Query["id_field"] != "external_id" {
		t.Fatalf("incorrect lookup: %s", raw)
	}
	data := &dwsEvidenceData{}
	svc := &Service{Data: data}
	ctx := svc.enrichItemContext(context.Background(), batch, change)
	if !knownEvidenceReferences(ctx)["71"] || len(data.queries) != 1 || len(data.queries[0].Filters) != 3 {
		t.Fatal("exact message enrichment failed", data.queries)
	}
	for _, tc := range []struct {
		ref  string
		want bool
	}{{`["71"]`, true}, {`[{"kind":"record_id","value":"72"}]`, true}, {`[{"kind":"external_id","value":"related"}]`, true}, {`["foreign-group"]`, false}, {`["unavailable"]`, false}} {
		if got := svc.validateItemEvidence(ctx, "owner", json.RawMessage(tc.ref), change); got != tc.want {
			t.Fatalf("%s: %v", tc.ref, got)
		}
	}
}
func TestDWSAttachmentDoesNotClaimPlatformRecordLookup(t *testing.T) {
	raw, err := filterInput(model.ControlBatch{Source: "dws"}, []model.ControlChange{{Source: "dws", Resource: "attachment", RecordID: "71:attachment"}})
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	if _, exists := input["evidence_query"]; exists {
		t.Fatalf("invented platform lookup: %s", raw)
	}
}
