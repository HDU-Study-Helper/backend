package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestMOOCRosterIsEvidenceWithoutIndependentControlItem(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "mooc-roster", model.RoleUser)
	repo := db.Platforms()
	connection, e := repo.Create(ctx, u.ID, "mooc")
	must(t, e)
	lease, e := repo.Acquire(ctx, model.DWSScope{UserID: u.ID, ConnectionID: connection.ID})
	must(t, e)
	must(t, repo.SaveAuth(ctx, lease, []byte("fixture-session"), "active", time.Now().Add(time.Hour)))
	round, e := db.Control().StartRound(ctx, "roster-test")
	must(t, e)
	ctx = model.WithControlRound(ctx, round)
	body := []byte(`{"kind":"discussion_reply_user","external_id":"reply-1","parent_id":"topic-1","fields":{"course_id":"course","termId":"term","topic_id":"topic-1","is_current_user":true,"participation_status":"reply_observed","body_status":"not_collected"}}`)
	hash := sha256.Sum256(body)
	record := model.PlatformRecord{Source: "mooc", Kind: "discussion_reply_user", ExternalID: "reply-1", ParentID: "topic-1", Hash: hex.EncodeToString(hash[:]), Content: body, ObservedAt: time.Now().UTC()}
	must(t, repo.Commit(ctx, lease, "discussion", []model.PlatformRecord{record}, []byte("next-session"), []byte("next-cursor")))
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM platform_records WHERE user_id=$1 AND kind='discussion_reply_user'`, u.ID).Scan(&count))
	if count != 1 {
		t.Fatal("roster evidence not persisted")
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE round_id=$1`, round).Scan(&count))
	if count != 0 {
		t.Fatal("reply roster expanded into standalone work")
	}
	state, e := repo.Load(ctx, lease, "discussion")
	must(t, e)
	if string(state.Cursor) != "next-cursor" {
		t.Fatal("roster page failed to advance discussion checkpoint")
	}
	invalid := record
	invalid.Source = "chaoxing"
	if repo.Commit(ctx, lease, "discussion", []model.PlatformRecord{invalid}, nil, nil) == nil {
		t.Fatal("mixed resource allowance escaped MOOC scope")
	}
}
