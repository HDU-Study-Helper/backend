package dws

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestAvailableGroupsPagination(t *testing.T) {
	good := map[string]any{"groups": []any{map[string]any{"conversationId": "cid-one", "name": "课程群"}}, "count": 1, "paginationKnown": true, "hasMore": true, "complete": false, "nextCursor": "next"}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		bad  bool
	}{
		{"next_page", func(m map[string]any) {}, false},
		{"numeric_cursor", func(m map[string]any) { m["nextCursor"] = 12 }, false},
		{"terminal", func(m map[string]any) { m["hasMore"] = false; m["complete"] = true; delete(m, "nextCursor") }, false},
		{"empty", func(m map[string]any) {
			m["groups"] = []any{}
			m["count"] = 0
			m["hasMore"] = false
			m["complete"] = true
		}, false},
		{"unknown_pagination", func(m map[string]any) { delete(m, "hasMore") }, true},
		{"partial", func(m map[string]any) { m["partial"] = true }, true},
		{"count_mismatch", func(m map[string]any) { m["count"] = 3 }, true},
		{"duplicate", func(m map[string]any) {
			m["groups"] = []any{map[string]any{"conversationId": "cid-one"}, map[string]any{"conversationId": "cid-one"}}
			m["count"] = 2
		}, true},
		{"missing_id", func(m map[string]any) { m["groups"] = []any{map[string]any{"name": "no id"}} }, true},
		{"repeated_cursor", func(m map[string]any) { m["nextCursor"] = "prior" }, true},
		{"missing_cursor", func(m map[string]any) { delete(m, "nextCursor") }, true},
		{"contradictory_complete", func(m map[string]any) { m["complete"] = true }, true},
		{"null_groups", func(m map[string]any) { m["groups"] = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range good {
				m[k] = v
			}
			tc.edit(m)
			b, _ := json.Marshal(m)
			_, err := parseAvailableGroups(b, "prior")
			if (err != nil) != tc.bad {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
func TestAvailableGroupsWorkspaceAndScope(t *testing.T) {
	s, repo, exec := testService(t)
	exec.pages = [][]byte{[]byte(`{"groups":[{"conversationId":"cid-one","name":"课程群"}],"count":1,"paginationKnown":true,"hasMore":false,"complete":true}`)}
	page, err := s.AvailableGroups(context.Background(), repo.scope, "cursor-1")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "cid-one" || !repo.released {
		t.Fatalf("page/cleanup: %+v %v", page, err)
	}
	want := []string{"chat", "+my-groups", "--limit", "100", "--profile", "corp:user", "-f", "json", "--cursor", "cursor-1"}
	if !reflect.DeepEqual(exec.calls[0], want) {
		t.Fatalf("arguments: %v", exec.calls)
	}
	_, err = s.AvailableGroups(context.Background(), model.DWSScope{UserID: "another", ConnectionID: repo.scope.ConnectionID}, "")
	if !errors.Is(err, model.ErrNotFound) || len(exec.calls) != 1 {
		t.Fatal("cross-owner must not execute")
	}
}
