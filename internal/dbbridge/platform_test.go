package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestPostgresPlatformIsolationAndAtomicity(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "platform_owner", model.RoleUser)
	v := fixtureUser(t, db, "platform_other", model.RoleUser)
	r := db.Platforms()
	c, e := r.Create(ctx, u.ID, "chaoxing")
	must(t, e)
	// Pending QR challenges belong to interactive auth polling, never the data worker.
	targets, e := r.Targets(ctx, "", 100)
	must(t, e)
	if len(targets) != 0 {
		t.Fatal("worker selected a pending challenge")
	}
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	foreign := model.DWSScope{UserID: v.ID, ConnectionID: c.ID}
	if _, e = r.Get(ctx, foreign); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner connection", e)
	}
	l, e := r.Acquire(ctx, scope)
	must(t, e)
	must(t, r.SaveAuth(ctx, l, []byte("cipher-session"), "active", time.Now().Add(time.Hour)))
	targets, e = r.Targets(ctx, "", 100)
	must(t, e)
	if len(targets) != 1 || targets[0].ID != c.ID {
		t.Fatal("active connection missing from worker")
	}
	if _, e = r.Acquire(ctx, scope); !errors.Is(e, model.ErrConflict) {
		t.Fatal("lease exclusion broken")
	}
	content := []byte(`{"kind":"course","external_id":"course-1","title":"脱敏课程"}`)
	h := sha256.Sum256(content)
	record := model.PlatformRecord{Source: "chaoxing", Kind: "course", ExternalID: "course-1", Hash: hex.EncodeToString(h[:]), Content: content, ObservedAt: time.Now().UTC()}
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{record}, []byte("session-new"), []byte("cursor-1")))
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{record}, []byte("session-new"), []byte("cursor-1")))
	rows, e := r.Records(ctx, scope, "course", 0, 20)
	must(t, e)
	if len(rows) != 1 {
		t.Fatal("dedup failed")
	}
	if _, e = r.Records(ctx, foreign, "course", 0, 20); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner records", e)
	}
	bad := record
	bad.Hash = "invalid"
	if e = r.Commit(ctx, l, "course", []model.PlatformRecord{bad}, []byte("bad-session"), []byte("bad-cursor")); e == nil {
		t.Fatal("bad hash accepted")
	}
	state, e := r.Load(ctx, l, "course")
	must(t, e)
	if string(state.Cursor) != "cursor-1" || string(state.Session) != "session-new" {
		t.Fatal("failed commit advanced state")
	}
	// A new authorization must never reuse an active platform identity.
	another, e := r.Create(ctx, u.ID, "chaoxing")
	must(t, e)
	if another.ID == c.ID {
		t.Fatal("active connection reused for another login")
	}
	must(t, r.Disconnect(ctx, scope))
	if e = r.Commit(ctx, l, "course", nil, nil, nil); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("disconnect failed fencing", e)
	}
	pendingScope := model.DWSScope{UserID: u.ID, ConnectionID: another.ID}
	lease, e := r.Acquire(ctx, pendingScope)
	must(t, e)
	disabled := true
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	disabled = false
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	if e = r.SaveAuth(ctx, lease, []byte("stale"), "active", time.Now()); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("disable then enable resurrected lease", e)
	}
}
