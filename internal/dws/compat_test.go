package dws

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLocalSourceTimes(t *testing.T) {
	for _, s := range []string{"2026-07-20 21:41:21", "2026-07-20 21:41:21.123"} {
		b, _ := json.Marshal(s)
		v, e := parseTime(b)
		if e != nil || v.Hour() != 13 {
			t.Fatalf("time %s: %v %v", s, v, e)
		}
	}
}
func TestOfficialResourceProjection(t *testing.T) {
	b := []byte(`{"contractVersion":"im.message-list.v1","count":1,"messages":[{"messageId":"m","conversationId":"g","messageType":"file","text":null,"createTime":"2026-01-01T00:10:00Z","resourceRefs":[{"type":"mediaId","resourceId":"r","download":{"arguments":{"type":"mediaId","resource-id":"r","message-id":"m","open-conversation-id":"g"},"ready":true}},{"type":"mediaId","resourceId":"nested","download":{"arguments":{"type":"mediaId","resource-id":"nested","message-id":"other","open-conversation-id":"g"},"ready":true}},{"type":"fileId","resourceId":"f","download":{"arguments":{"type":"fileId","resource-id":"f"},"ready":true}}]}],"complete":true,"hasMore":false,"paginationKnown":true}`)
	_, items, e := parsePage(b, "g", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	var o Observation
	_ = json.Unmarshal(items[0].Content, &o)
	if !o.ResourceRefs[0].Downloadable || o.ResourceRefs[1].Downloadable || !o.ResourceRefs[2].Downloadable {
		t.Fatal("resource ownership incorrect")
	}
}
func TestFailedSyncSavesRefreshedWorkspaceWithoutWatermark(t *testing.T) {
	s, r, x := testService(t)
	old := append([]byte{}, r.sealed...)
	x.pages = [][]byte{[]byte(`{"error":"business failure"}`)}
	if e := s.Sync(context.Background(), r.scope, "g", "job"); e == nil {
		t.Fatal("expected failure")
	}
	if !r.failedBeforeRelease || len(r.commits) != 0 || string(old) == string(r.sealed) {
		t.Fatal("failure did not fence job and save workspace")
	}
}
func TestMessageDTOIsObject(t *testing.T) {
	items, e := messageDTOs([]model.Message{{Content: []byte(`{"text":"hello"}`)}})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(items)
	var decoded []map[string]any
	_ = json.Unmarshal(b, &decoded)
	if _, ok := decoded[0]["content"].(map[string]any); !ok {
		t.Fatal("content is not JSON object")
	}
	if _, e = messageDTOs([]model.Message{{Content: []byte(`invalid`)}}); e == nil {
		t.Fatal("invalid JSON accepted")
	}
}
