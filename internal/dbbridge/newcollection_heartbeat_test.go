package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

func TestCollectionHeartbeatOnlyRenewsOwnedRefreshingRound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Control()
	user := fixtureUser(t, db, "heartbeat", model.RoleUser)
	heartbeat := repo.(model.ControlRoundHeartbeater)
	round, err := repo.StartRound(ctx, "collector")
	must(t, err)
	other, err := repo.StartRound(ctx, "other")
	must(t, err)
	sameOwner, err := repo.StartRound(ctx, "collector")
	must(t, err)
	_, err = db.pool.Exec(ctx, `UPDATE control_rounds SET heartbeat=now()-interval '46 seconds' WHERE id=ANY($1::text[])`, []string{round, other, sameOwner})
	must(t, err)
	var before time.Time
	must(t, db.pool.QueryRow(ctx, `SELECT heartbeat FROM control_rounds WHERE id=$1`, round).Scan(&before))
	if err := heartbeat.HeartbeatRound(ctx, round, "wrong-owner"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("wrong owner should be rejected: %v", err)
	}
	var unchanged time.Time
	must(t, db.pool.QueryRow(ctx, `SELECT heartbeat FROM control_rounds WHERE id=$1`, round).Scan(&unchanged))
	if !before.Equal(unchanged) {
		t.Fatal("wrong owner renewed the round")
	}
	must(t, heartbeat.HeartbeatRound(ctx, round, "collector"))
	var renewed time.Time
	must(t, db.pool.QueryRow(ctx, `SELECT heartbeat FROM control_rounds WHERE id=$1`, round).Scan(&renewed))
	if !renewed.After(before) {
		t.Fatal("owned round heartbeat did not advance")
	}
	for _, id := range []string{other, sameOwner} {
		var status string
		var stale bool
		must(t, db.pool.QueryRow(ctx, `SELECT status,heartbeat<now()-interval '45 seconds' FROM control_rounds WHERE id=$1`, id).Scan(&status, &stale))
		if status != "refreshing" || !stale {
			t.Fatalf("scoped heartbeat touched unrelated round %s: status=%s stale=%v", id, status, stale)
		}
	}
	// Another worker's housekeeping must leave the renewed collection active.
	must(t, repo.Heartbeat(ctx, "worker"))
	var status string
	must(t, db.pool.QueryRow(ctx, `SELECT status FROM control_rounds WHERE id=$1`, round).Scan(&status))
	if status != "refreshing" {
		t.Fatalf("live collection was interrupted: %s", status)
	}
	page := func(record string) {
		t.Helper()
		must(t, db.transaction(ctx, func(tx pgx.Tx) error {
			return captureControl(model.WithControlRound(ctx, round), tx, user.ID, "dws", []model.ControlChange{{Operation: "created", Resource: "message", ConnectionID: "heartbeat-connection", GroupID: "heartbeat-group", RecordID: record, After: json.RawMessage(`{"text":"fixture"}`)}})
		}))
	}
	page("first-page")
	must(t, repo.Seal(ctx))
	var batchCount int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_batches WHERE round_id=$1`, round).Scan(&batchCount))
	if batchCount != 0 {
		t.Fatal("worker sealed a collection that is still refreshing")
	}
	page("second-page")
	must(t, repo.FinishRound(ctx, round, nil, false))
	must(t, repo.Seal(ctx))
	var changeCount int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(change_count),0)::int FROM control_batches WHERE round_id=$1`, round).Scan(&batchCount, &changeCount))
	if batchCount != 1 || changeCount != 2 {
		t.Fatalf("collection pages were split or lost: batches=%d changes=%d", batchCount, changeCount)
	}
	if err := heartbeat.HeartbeatRound(ctx, round, "collector"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("finished round should not be renewed: %v", err)
	}
	if err := heartbeat.HeartbeatRound(ctx, other, "other"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("interrupted round should not be revived: %v", err)
	}
	if err := heartbeat.HeartbeatRound(ctx, "missing", "collector"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("missing round should be rejected: %v", err)
	}
	for _, args := range [][2]string{{"", "collector"}, {round, ""}} {
		if err := heartbeat.HeartbeatRound(ctx, args[0], args[1]); !errors.Is(err, model.ErrInvalidArgument) {
			t.Fatalf("empty identity should be rejected: %v", err)
		}
	}
}
