package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"testing"
	"time"
)

func TestPostgresDiscussionCheckpointsAreCourseScoped(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "discussion_owner", model.RoleUser)
	r := db.Platforms()
	c, e := r.Create(ctx, u.ID, "chaoxing")
	must(t, e)
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	l, e := r.Acquire(ctx, scope)
	must(t, e)
	must(t, r.SaveAuth(ctx, l, []byte("cipher"), "active", time.Now().Add(time.Hour)))
	l.CheckpointKey = "discussion:course-a"
	must(t, r.Commit(ctx, l, "discussion", nil, []byte("cipher"), []byte("a-next")))
	l.CheckpointKey = "discussion:course-b"
	must(t, r.Commit(ctx, l, "discussion", nil, []byte("cipher"), []byte("b-next")))
	for key, want := range map[string]string{"discussion:course-a": "a-next", "discussion:course-b": "b-next", "discussion": ""} {
		state, e := r.Load(ctx, l, key)
		must(t, e)
		if string(state.Cursor) != want {
			t.Fatal("course cursor overwritten", key)
		}
	}
	overview, e := db.Preview().Overview(ctx, u.ID)
	must(t, e)
	if len(overview) != 1 || len(overview[0].Resources) != 1 || overview[0].Resources[0].Kind != "discussion" {
		t.Fatal("partition keys leaked as resource names")
	}
}
