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

func TestPostgresPreviewStatusAndOwner(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "preview_owner", model.RoleUser)
	other := fixtureUser(t, db, "preview_other", model.RoleUser)
	repo := db.Platforms()
	c, e := repo.Create(ctx, owner.ID, "chaoxing")
	must(t, e)
	scope := model.DWSScope{UserID: owner.ID, ConnectionID: c.ID}
	lease, e := repo.Acquire(ctx, scope)
	must(t, e)
	must(t, repo.SaveAuth(ctx, lease, []byte("synthetic-encrypted-session"), "active", time.Now().Add(time.Hour)))
	observer := repo.(model.SyncObserver)
	must(t, observer.StartSync(ctx, lease, "course"))
	overview, e := db.Preview().Overview(ctx, owner.ID)
	must(t, e)
	if len(overview) != 1 || len(overview[0].Resources) != 1 || !overview[0].Resources[0].InProgress {
		t.Fatal("running status absent")
	}
	for _, title := range []string{"old", "new"} {
		b := []byte(`{"kind":"course","external_id":"course1","title":"` + title + `"}`)
		h := sha256.Sum256(b)
		v := model.PlatformRecord{Source: "chaoxing", Kind: "course", ExternalID: "course1", Hash: hex.EncodeToString(h[:]), Content: b, ObservedAt: time.Now().UTC()}
		must(t, repo.Commit(ctx, lease, "course", []model.PlatformRecord{v}, []byte("cipher"), []byte("ciphercursor")))
	}
	must(t, observer.FinishSync(ctx, lease, "course", "", 42, 1, true))
	overview, e = db.Preview().Overview(ctx, owner.ID)
	must(t, e)
	r := overview[0].Resources[0]
	if r.RecordCount != 1 || r.EntityCount != 1 || r.LastSuccessAt == nil || r.LastAttemptAt == nil || r.LastDurationMS == nil || *r.LastDurationMS != 42 || r.HasMore == nil || !*r.HasMore || r.InProgress {
		t.Fatal("incorrect stats", r)
	}
	must(t, observer.StartSync(ctx, lease, "course"))
	must(t, observer.FinishSync(ctx, lease, "course", "parse_failed", 3, 0, false))
	overview, e = db.Preview().Overview(ctx, owner.ID)
	must(t, e)
	r = overview[0].Resources[0]
	if r.LastSuccessAt == nil || r.LastErrorCode != "parse_failed" || r.LastPageCount != nil {
		t.Fatal("failed attempt concealed or success lost")
	}

	must(t, observer.StartSync(ctx, lease, "course"))
	must(t, observer.FinishSync(ctx, lease, "course", "verification_required", 2, 0, false))
	must(t, repo.Fail(ctx, lease, "verification_required"))
	connection, e := repo.Get(ctx, scope)
	must(t, e)
	overview, e = db.Preview().Overview(ctx, owner.ID)
	must(t, e)
	if connection.ErrorCode != "verification_required" || connection.State != "active" || overview[0].Resources[0].LastErrorCode != "verification_required" || overview[0].Resources[0].LastSuccessAt == nil {
		t.Fatal("verification cause lost or login incorrectly expired")
	}
	foreign, e := db.Preview().Overview(ctx, other.ID)
	must(t, e)
	if len(foreign) != 0 {
		t.Fatal("other user's stats leaked")
	}
	if _, e = db.Preview().Records(ctx, other.ID, "chaoxing", c.ID, "course", 0, 20); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("other user's records leaked")
	}
	if _, e = db.Preview().Records(ctx, owner.ID, "mooc", c.ID, "course", 0, 20); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("source filter bypass")
	}
	records, e := db.Preview().Records(ctx, owner.ID, "chaoxing", c.ID, "course", 0, 20)
	must(t, e)
	if len(records) != 1 {
		t.Fatal("expected one latest record")
	}
	must(t, repo.Disconnect(ctx, scope))
	if e = observer.FinishSync(ctx, lease, "course", "", 1, 1, false); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("stale stats overwrote state")
	}
}
