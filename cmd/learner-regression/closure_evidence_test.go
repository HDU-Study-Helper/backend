package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestClosureValidateEvidenceFindsNestedMappingAndCaches(t *testing.T) {
	calls := 0
	fetch := func(_ context.Context, id, connection string) (map[string]any, error) {
		calls++
		if id != "99079" || connection != "course-connection" {
			t.Fatal("reader did not receive both exact reference identifiers", id, connection)
		}
		return map[string]any{"id": id, "connection_id": connection, "external_id": "4143"}, nil
	}
	raw := json.RawMessage(`{"artifacts":[{"data":{"evidence":[{"dataset":"platform_records","id":"99079","connection_id":"course-connection","external_id":"4143"}]}}],"memories":[{"source_refs":[{"dataset":"platform_records","id":"99079","connection_id":"course-connection","external_id":"4143"}]}]}`)
	if err := closureValidateEvidence(context.Background(), raw, fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("duplicate record references were read repeatedly", calls)
	}
	// A conflicting duplicate must still be compared with the cached real row.
	bad := json.RawMessage(strings.Replace(string(raw), `"external_id":"4143"`, `"external_id":"4142"`, 1))
	if err := closureValidateEvidence(context.Background(), bad, fetch); err == nil {
		t.Fatal("invented external ID accepted")
	}
}

func TestClosureValidateEvidenceRejectsWrongOrUnavailableMapping(t *testing.T) {
	for _, mode := range []string{"external", "connection", "central", "unavailable", "missing connection", "missing reader"} {
		t.Run(mode, func(t *testing.T) {
			ref := map[string]any{"dataset": "platform_records", "id": "99079", "connection_id": "course-connection", "external_id": "4143"}
			row := map[string]any{"id": "99079", "connection_id": "course-connection", "external_id": "4143"}
			switch mode {
			case "external":
				row["external_id"] = "4142"
			case "connection":
				row["connection_id"] = "foreign-connection"
			case "central":
				row["id"] = "99078"
			case "missing connection":
				delete(ref, "connection_id")
			}
			fetch := closureEvidenceFetch(func(context.Context, string, string) (map[string]any, error) {
				if mode == "unavailable" {
					return nil, errors.New("record is not authorized")
				}
				return row, nil
			})
			if mode == "missing reader" {
				fetch = nil
			}
			raw, _ := json.Marshal(ref)
			if err := closureValidateEvidence(context.Background(), raw, fetch); err == nil {
				t.Fatal("unverified reference mapping accepted", mode)
			}
		})
	}
}

func TestClosureValidateEvidenceSkipsUnscopedAndExternalIDFreeObjects(t *testing.T) {
	raw := json.RawMessage(`[{"dataset":"platform_records","id":"99079"},{"dataset":"platform_records","id":"99079","external_id":""},{"dataset":"academic_items","id":"99079","external_id":"4143"},{"id":"entity-id","external_id":"4143"},{"dataset":"platform_records","id":123,"external_id":"4143"}]`)
	if err := closureValidateEvidence(context.Background(), raw, nil); err != nil {
		t.Fatal("validator expanded outside explicit string-ID platform references", err)
	}
}

func TestClosureValidateEvidenceCachesByConnectionAndRejectsConflictingDuplicate(t *testing.T) {
	calls := 0
	fetch := func(_ context.Context, id, connection string) (map[string]any, error) {
		calls++
		return map[string]any{"id": id, "connection_id": connection, "external_id": connection + "-native"}, nil
	}
	raw := json.RawMessage(`[{"dataset":"platform_records","id":"1","connection_id":"a","external_id":"a-native"},{"dataset":"platform_records","id":"1","connection_id":"b","external_id":"b-native"}]`)
	if err := closureValidateEvidence(context.Background(), raw, fetch); err != nil || calls != 2 {
		t.Fatal("cache crossed connection boundaries", calls, err)
	}
	conflict := json.RawMessage(`[{"dataset":"platform_records","id":"1","connection_id":"a","external_id":"a-native"},{"dataset":"platform_records","id":"1","connection_id":"a","external_id":"different"}]`)
	calls = 0
	if err := closureValidateEvidence(context.Background(), conflict, fetch); err == nil || calls != 1 {
		t.Fatal("conflicting duplicate bypassed cached validation", calls, err)
	}
}

func TestClosureValidateEvidenceMissingDatasetCannotHideKnownMapping(t *testing.T) {
	fetch := func(_ context.Context, id, connection string) (map[string]any, error) {
		if connection == "legacy" {
			return nil, errors.New("unknown scope")
		}
		return map[string]any{"id": id, "connection_id": connection, "external_id": "4143"}, nil
	}
	for _, dataset := range []string{`"dataset":"platform_records",`, ""} {
		raw := json.RawMessage(`{` + dataset + `"id":"99079","connection_id":"course","external_id":"4142"}`)
		if closureValidateEvidence(context.Background(), raw, fetch) == nil {
			t.Fatal("removed dataset bypassed known authorized mapping")
		}
	}
	if err := closureValidateEvidence(context.Background(), json.RawMessage(`{"id":"old","connection_id":"legacy","external_id":"opaque"}`), fetch); err != nil {
		t.Fatal("unknown legacy scope rejected", err)
	}
}

func TestClosureValidateEvidenceHonorsCancellationAndValidJSON(t *testing.T) {
	if err := closureValidateEvidence(context.Background(), json.RawMessage(`{`), nil); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := closureValidateEvidence(ctx, json.RawMessage(`{}`), nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
