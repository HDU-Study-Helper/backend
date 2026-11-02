package dws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParsePageIntegrity(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	base := `{"contractVersion":"im.message-list.v1","count":1,"messages":[{"messageId":"m1","conversationId":"g1","messageType":"text","text":"考试通知：提交作业","createTime":"2026-01-01T00:10:00Z","resourceRefs":[]}],"complete":true,"hasMore":false,"paginationKnown":true,"failedCount":0,"failures":[],"partial":false}`
	_, items, e := parsePage([]byte(base), "g1", start, end)
	if e != nil || len(items) != 1 {
		t.Fatalf("parse: %v", e)
	}
	var o Observation
	if e = json.Unmarshal(items[0].Content, &o); e != nil {
		t.Fatal(e)
	}
	if strings.Join(o.Labels, ",") != "assignment,exam,notice" || o.Verification != "unconfirmed" {
		t.Fatalf("labels: %+v", o)
	}
	_, again, e := parsePage([]byte(base), "g1", start, end)
	if e != nil || again[0].ContentHash != items[0].ContentHash {
		t.Fatal("unstable hash")
	}
	for name, b := range map[string]string{
		"decrypt":            strings.Replace(base, `"failedCount":0`, `"decryptFailedCount":1,"failedCount":0`, 1),
		"partial":            strings.Replace(base, `"partial":false`, `"partial":true`, 1),
		"unknown pagination": strings.Replace(base, `"paginationKnown":true`, `"paginationKnown":false`, 1),
		"wrong group":        strings.Replace(base, `"conversationId":"g1"`, `"conversationId":"g2"`, 1),
		"business failure":   strings.Replace(base, `"partial":false`, `"partial":false,"success":false`, 1),
		"envelope":           `{"ok":true,"data":` + base + `}`,
		"missing completion": strings.Replace(base, `"complete":true,`, ``, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, e := parsePage([]byte(b), "g1", start, end); e == nil {
				t.Fatal("accepted invalid page")
			}
		})
	}
}
func TestEmptyContinuation(t *testing.T) {
	b := []byte(`{"contractVersion":"im.message-list.v1","count":0,"messages":[],"complete":false,"hasMore":true,"paginationKnown":true,"nextPageToken":"second"}`)
	p, items, e := parsePage(b, "g", time.Now(), time.Now())
	if e != nil || len(items) != 0 || p.NextPageToken != "second" {
		t.Fatal(e)
	}
}
func TestProfileScope(t *testing.T) {
	if !validProfile("corp:user", "corp", "user") {
		t.Fatal("valid profile rejected")
	}
	for _, v := range []string{"corp:user,other:user", "corp:user:extra", "corp:user\n"} {
		if validProfile(v, "corp", "user") {
			t.Fatal("invalid multi profile accepted")
		}
	}
}
func TestResourceKeyStable(t *testing.T) {
	r := ResourceRef{Type: "mediaId", ResourceID: "r", MessageID: "m", OpenConversationID: "g"}
	key := resourceKey(r)
	r.AttachmentID = key
	if resourceKey(r) != key {
		t.Fatal("key changes with projection")
	}
}
