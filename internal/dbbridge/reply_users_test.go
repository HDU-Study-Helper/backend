package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestPostgresReplyUserIdentityIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "reply_owner", model.RoleUser)
	v := fixtureUser(t, db, "reply_other", model.RoleUser)
	r := db.Platforms()
	c, e := r.Create(ctx, u.ID, "chaoxing")
	must(t, e)
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	l, e := r.Acquire(ctx, scope)
	must(t, e)
	must(t, r.SaveAuth(ctx, l, []byte("cipher"), "active", time.Now().Add(time.Hour)))
	l.CheckpointKey = replyPartitionKey("course", "class", "topic")
	records := []model.PlatformRecord{}
	for _, id := range []string{"reply1", "reply2"} {
		b, _ := json.Marshal(map[string]any{"kind": "discussion_reply_user", "fields": map[string]any{"course_id": "course", "class_id": "class", "topic_id": "topic", "user_id": "user1", "user_name": "脱敏用户", "anonymous": false}})
		h := sha256.Sum256(b)
		records = append(records, model.PlatformRecord{Source: "chaoxing", Kind: "discussion_reply_user", ExternalID: id, ParentID: "topic", Hash: hex.EncodeToString(h[:]), Content: b, ObservedAt: time.Now().UTC()})
	}
	must(t, r.Commit(ctx, l, "discussion_reply_user", records, []byte("cipher"), nil))
	must(t, r.Commit(ctx, l, "discussion_reply_user", records, []byte("cipher"), nil))
	reader := r.(model.ReplyUserRepository)
	out, e := reader.ReplyUsers(ctx, scope, "course", "class", "topic")
	must(t, e)
	if len(out.Items) != 1 || out.Items[0].ReplyCount != 2 || out.ReplyObservationCount != 2 || out.LastFetchedAt == nil {
		t.Fatal("reply users not deduplicated", out)
	}
	foreign := scope
	foreign.UserID = v.ID
	if _, e = reader.ReplyUsers(ctx, foreign, "course", "class", "topic"); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("foreign reply users leaked")
	}
	out, e = reader.ReplyUsers(ctx, scope, "course", "otherclass", "topic")
	must(t, e)
	if len(out.Items) != 0 {
		t.Fatal("cross-class reply users leaked")
	}
}
