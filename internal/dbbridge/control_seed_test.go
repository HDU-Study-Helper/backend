package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestSealSeedsEveryPendingBatchItemWithoutOverwrite(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "seed-owner", model.RoleUser)
	store := db.Control()
	round, e := store.StartRound(ctx, "seed")
	must(t, e)
	must(t, db.transaction(ctx, func(tx pgx.Tx) error {
		for _, source := range []string{"chaoxing", "mooc"} {
			if e := captureControl(model.WithControlRound(ctx, round), tx, u.ID, source, []model.ControlChange{{RecordID: "r1", Resource: "notice", After: json.RawMessage(`{"title":"通知一"}`), OccurredAt: time.Now()}, {RecordID: "r2", Resource: "notice", After: json.RawMessage(`{"title":"通知二"}`), OccurredAt: time.Now()}}); e != nil {
				return e
			}
		}
		return nil
	}))
	must(t, store.FinishRound(ctx, round, nil, false))
	must(t, store.Seal(ctx))
	items := store.(model.ControlItemRepository)
	counts, e := items.ItemCounts(ctx, round)
	must(t, e)
	if counts["PENDING"] != 4 {
		t.Fatal(counts)
	}
	list, e := items.ListItems(ctx, model.ControlListQuery{RoundID: round})
	must(t, e)
	list[0].Status = "COMPLETED"
	list[0].Reason = "已完成保留"
	must(t, items.SaveItem(ctx, list[0]))
	must(t, store.Seal(ctx))
	counts, e = items.ItemCounts(ctx, round)
	must(t, e)
	if counts["PENDING"] != 3 || counts["COMPLETED"] != 1 {
		t.Fatal(counts)
	}
	v, e := items.GetItem(ctx, list[0].ID)
	must(t, e)
	if v.Reason != "已完成保留" {
		t.Fatal(v)
	}
}
