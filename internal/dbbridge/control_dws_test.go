package dbbridge

import (
	"context"
	"encoding/json"
	"testing"

	"academic/backend/internal/model"
)

func TestPostgresControlDWSGroupsKeepConnectionAndConversation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "dws-groups", model.RoleUser)
	round, err := db.Control().StartRound(ctx, "fixture")
	must(t, err)
	// Interleave groups so adjacency cannot accidentally provide isolation.
	changes := []model.ControlChange{
		{ID: "a-first", Resource: "message", ConnectionID: "conn-a", GroupID: "group-a", After: json.RawMessage(`{"conversationId":"group-a","text":"first"}`)},
		{ID: "b", Resource: "message", ConnectionID: "conn-a", GroupID: "group-b", After: json.RawMessage(`{"conversationId":"group-b","text":"other group"}`)},
		{ID: "a-second", Resource: "attachment", ConnectionID: "conn-a", GroupID: "group-a", After: json.RawMessage(`{"name":"notes.pdf"}`)},
		{ID: "other-connection", Resource: "message", ConnectionID: "conn-b", GroupID: "group-a", After: json.RawMessage(`{"conversationId":"group-a","text":"same group ID, another connection"}`)},
		{ID: "snapshot-group", Resource: "message", ConnectionID: "conn-a", After: json.RawMessage(`{"conversationId":"group-a","text":"legacy snapshot identity"}`)},
		{ID: "missing", Resource: "message", ConnectionID: "conn-a", After: json.RawMessage(`{"text":"unresolved"}`)},
		{ID: "conflicting", Resource: "message", ConnectionID: "conn-a", GroupID: "group-a", After: json.RawMessage(`{"conversationId":"group-b","text":"conflicting identity"}`)},
	}
	controlCourseEvents(t, db, round, u.ID, "dws", changes)
	must(t, db.Control().FinishRound(ctx, round, nil, false))
	must(t, db.Control().Seal(ctx))
	must(t, db.Control().Seal(ctx))
	batches := controlCourseBatches(t, db, round)
	if len(batches) != 5 {
		t.Fatalf("group isolation or unresolved events lost: %+v", batches)
	}
	want := map[[2]string]int{{"conn-a", "group-a"}: 3, {"conn-a", "group-b"}: 1, {"conn-b", "group-a"}: 1}
	total, blocked := 0, 0
	for _, batch := range batches {
		total += batch.Count
		if batch.Scope == nil {
			t.Fatal("new DWS batch has no frozen scope", batch)
		}
		if batch.Status == "blocked" {
			blocked++
			if batch.Error != "DWS_GROUP_UNRESOLVED" || batch.Count != 1 || batch.Scope.Kind != "unresolved_group" || (batch.Changes[0].ID != "missing" && batch.Changes[0].ID != "conflicting") {
				t.Fatal("unresolved identity was merged or misclassified", batch)
			}
			continue
		}
		key := [2]string{batch.Scope.ConnectionID, batch.Scope.GroupID}
		if batch.Status != "pending" || batch.Scope.Kind != "group" || want[key] != batch.Count {
			t.Fatal("group boundary changed", batch)
		}
		for _, change := range batch.Changes {
			if change.ConnectionID != key[0] || change.GroupID != "" && change.GroupID != key[1] {
				t.Fatal("foreign group or connection entered batch", change, batch.Scope)
			}
		}
		delete(want, key)
	}
	if total != len(changes) || blocked != 2 || len(want) != 0 {
		t.Fatal("events duplicated, lost or grouped incorrectly", total, blocked, want)
	}
}
