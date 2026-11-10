package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestControlClaimPrioritizesTriggerBeforeOlderItemStage(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user := fixtureUser(t, db, "claim-phases", model.RoleAdmin)
	repo := db.Control()
	round, e := repo.StartRound(ctx, "instance")
	must(t, e)
	now := time.Now().UTC()
	for _, b := range []struct {
		id, stage string
		at        time.Time
	}{{"old-items", "items", now.Add(-time.Hour)}, {"new-trigger", "trigger", now}, {"later-trigger", "trigger", now.Add(time.Second)}} {
		_, e = db.pool.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,status,stage,changes,change_count,created_at) VALUES($1,$2,$3,'chaoxing','queued',$4,'[]',0,$5)`, b.id, round, user.ID, b.stage, b.at)
		must(t, e)
	}
	for _, want := range []string{"new-trigger", "later-trigger", "old-items"} {
		got, e := repo.Claim(ctx, "instance")
		must(t, e)
		if got.ID != want {
			t.Fatalf("claim order: got %s want %s", got.ID, want)
		}
	}
	if _, e = repo.Claim(ctx, "instance"); !errors.Is(e, model.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestControlItemStageYieldKeepsAttemptFencing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user := fixtureUser(t, db, "phase-fence", model.RoleAdmin)
	repo := db.Control()
	round, e := repo.StartRound(ctx, "instance")
	must(t, e)
	_, e = db.pool.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,status,changes,change_count) VALUES('phase',$1,$2,'chaoxing','queued','[]',0)`, round, user.ID)
	must(t, e)
	first, e := repo.Claim(ctx, "one")
	must(t, e)
	first.Stage = "items"
	first.Status = "queued"
	first.Trigger = []byte(`{"decision":"TRIGGER","reason":"待处理","relevant_change_ids":["change"]}`)
	must(t, repo.SetBatch(ctx, first))
	second, e := repo.Claim(ctx, "two")
	must(t, e)
	var saved, original any
	must(t, json.Unmarshal(second.Trigger, &saved))
	must(t, json.Unmarshal(first.Trigger, &original))
	if second.Stage != "items" || second.Attempt != first.Attempt+1 || !reflect.DeepEqual(saved, original) {
		t.Fatal(second)
	}
	first.Status = "failed"
	if !errors.Is(repo.SetBatch(ctx, first), model.ErrConflict) {
		t.Fatal("old Trigger attempt overwrote item phase")
	}
	second.Status = "cancelled"
	must(t, repo.SetBatch(ctx, second))
}
