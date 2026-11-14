package runcontrol

import (
	"academic/backend/internal/model"
	"encoding/json"
	"testing"
	"time"
)

func TestFilterInputPreservesStructuredEvidenceIdentities(t *testing.T) {
	at := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	changes := []model.ControlChange{
		{ID: "change-a", RecordID: "48413", ConnectionID: "connection-a", Source: "chaoxing", Resource: "material", Operation: "created", OccurredAt: at, After: json.RawMessage(`{"external_id":"1308560821","content":"资料正文"}`)},
		{ID: "change-b", RecordID: "48412", ConnectionID: "connection-b", Source: "chaoxing", Resource: "material", Operation: "deleted", OccurredAt: at, Before: json.RawMessage(`{"external_id":"native-old"}`)},
		{ID: "change-c", RecordID: "48411", ConnectionID: "connection-a", Source: "chaoxing", Resource: "material", Operation: "updated", OccurredAt: at, After: json.RawMessage(`{"external_id":"native-new"}`), Before: json.RawMessage(`{"external_id":"native-before"}`)},
	}
	raw, err := filterInput(model.ControlBatch{ID: "batch-a", Source: "chaoxing", RoundID: "round-a", Trigger: json.RawMessage(`{"decision":"TRIGGER"}`)}, changes)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Content       []filterChange    `json:"content"`
		Refs          []string          `json:"evidence_refs"`
		RefKind       string            `json:"evidence_ref_kind"`
		Links         []filterEvidence  `json:"evidence_links"`
		RecordIDs     []string          `json:"record_ids"`
		ChangeIDs     []string          `json:"change_ids"`
		ExternalIDs   []string          `json:"external_ids"`
		ConnectionIDs []string          `json:"connection_ids"`
		EvidenceQuery map[string]string `json:"evidence_query"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("content must be an array, not encoded JSON text: %v", err)
	}
	if len(input.Content) != 3 || len(input.Links) != 3 || input.RefKind != "change_id" {
		t.Fatalf("missing structured evidence: %s", raw)
	}
	for i, nativeID := range []string{"1308560821", "native-old", "native-new"} {
		got, want := input.Content[i], changes[i]
		if got.ID != want.ID || got.RecordID != want.RecordID || got.ConnectionID != want.ConnectionID || got.ExternalID != nativeID || input.Links[i] != (filterEvidence{ChangeID: want.ID, RecordID: want.RecordID, ConnectionID: want.ConnectionID, ExternalID: nativeID}) {
			t.Fatalf("identities lost or conflated at index %d: %s", i, raw)
		}
		if input.Refs[i] != want.ID || input.ChangeIDs[i] != want.ID || input.RecordIDs[i] != want.RecordID || input.ExternalIDs[i] != nativeID || input.ConnectionIDs[i] != want.ConnectionID {
			t.Fatal("top-level references do not match change identities")
		}
	}
	if string(input.Content[0].After) != string(changes[0].After) || input.EvidenceQuery["id_value_from"] != "evidence_links[].record_id" || input.EvidenceQuery["id_field"] != "id" {
		t.Fatal("raw evidence or exact record lookup guidance lost")
	}
}

func TestTriggerOutputRejectsMissingOrInventedEvidence(t *testing.T) {
	changes := []model.ControlChange{{ID: "a"}, {ID: "b"}}
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{`{"decision":"IGNORE","reason":"重复","relevant_change_ids":[]}`, true},
		{`{"decision":"TRIGGER","reason":"新通知","relevant_change_ids":["a","b"]}`, true},
		{`{"decision":"UNCERTAIN","reason":"待核查","relevant_change_ids":["b"]}`, true},
		{`{"decision":"TRIGGER","reason":"x","relevant_change_ids":["unknown"]}`, false},
		{`{"decision":"TRIGGER","reason":"x","relevant_change_ids":["a","a"]}`, false},
		{`{"decision":"TRIGGER","reason":"x","relevant_change_ids":["b","a"]}`, true},
		{`{"decision":"IGNORE","reason":"x","relevant_change_ids":["a"]}`, false},
		{`{"decision":"IGNORE","reason":"x"}`, false},
		{`{"decision":"TRIGGER","reason":"x","relevant_change_ids":[]}`, false},
		{`{"decision":"IGNORE","reason":"x","relevant_change_ids":[],"extra":true}`, false},
		{`{"decision":"IGNORE","reason":"x","relevant_change_ids":[]} {}`, false},
	} {
		_, e := validateTrigger(json.RawMessage(tc.raw), changes)
		if (e == nil) != tc.ok {
			t.Errorf("validation mismatch %s: %v", tc.raw, e)
		}
	}
}
