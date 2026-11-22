package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"reflect"
	"testing"
	"time"
)

func TestControlRoundProgressGuard(t *testing.T) {
	p := ControlRoundProgress{Total: 2, RoundStatus: "completed", Batches: map[string]int64{"queued": 1, "blocked": 1}}
	if p.Preflight() != nil || p.Done() {
		t.Fatal(p)
	}
	p.Batches = map[string]int64{"completed": 1, "blocked": 1}
	if !p.Done() {
		t.Fatal(p)
	}
	for _, change := range []string{"other queued", "other running", "running", "pending", "unsealed", "refreshing", "unknown"} {
		q := p
		q.Batches = map[string]int64{}
		for k, v := range p.Batches {
			q.Batches[k] = v
		}
		switch change {
		case "other queued":
			q.OtherQueued = 1
		case "other running":
			q.OtherRunning = 1
		case "running":
			q.Batches["running"] = 1
		case "pending":
			q.Batches["pending"] = 1
		case "unsealed":
			q.Unsealed = 1
		case "refreshing":
			q.RoundStatus = "refreshing"
		case "unknown":
			q.Batches["new-state"] = 1
		}
		if q.Preflight() == nil {
			t.Fatal("guard accepted", change)
		}
	}
}
func TestPauseControlRoundQueueBacksUpBeforeCommitAndPreservesRunning(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "drain", model.RoleAdmin)
	first, second := newID(), newID()
	_, e := db.pool.Exec(ctx, `INSERT INTO control_rounds(id,status)VALUES($1,'completed'),($2,'completed')`, first, second)
	must(t, e)
	queued, running, other := newID(), newID(), newID()
	for _, v := range []struct{ id, round, status string }{{queued, first, "queued"}, {running, first, "running"}, {other, second, "queued"}} {
		_, e = db.pool.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,status,stage,changes,change_count,attempt,trigger_output,filter_output,error_code)VALUES($1,$2,$3,'ucampus',$4,'items','[]',0,3,'{"kept":true}','{"items":[]}','OLD_CODE')`, v.id, v.round, u.ID, v.status)
		must(t, e)
	}
	original, e := db.Control().GetBatch(ctx, queued)
	must(t, e)
	_, e = db.PauseControlRoundQueue(ctx, first, func(b []model.ControlBatch) error {
		if len(b) != 1 || !reflect.DeepEqual(b[0], original) {
			t.Fatal("incomplete backup", b)
		}
		return errors.New("disk full")
	})
	if e == nil {
		t.Fatal("failed backup changed queue")
	}
	unchanged, e := db.Control().GetBatch(ctx, queued)
	must(t, e)
	if !reflect.DeepEqual(original, unchanged) {
		t.Fatal("backup error was not atomic")
	}
	backed := false
	n, e := db.PauseControlRoundQueue(ctx, first, func(b []model.ControlBatch) error { backed = true; return nil })
	must(t, e)
	if n != 1 || !backed {
		t.Fatal(n, backed)
	}
	paused, e := db.Control().GetBatch(ctx, queued)
	must(t, e)
	if paused.Status != "pending" || paused.Error != "DEPLOYMENT_DRAIN" || paused.Stage != original.Stage || paused.Attempt != original.Attempt || string(paused.Trigger) != string(original.Trigger) || string(paused.Filter) != string(original.Filter) {
		t.Fatal("drain changed execution data", paused)
	}
	for _, id := range []string{running, other} {
		b, e := db.Control().GetBatch(ctx, id)
		must(t, e)
		want := "queued"
		if id == running {
			want = "running"
		}
		if b.Status != want || b.Error != "OLD_CODE" {
			t.Fatal("changed running/unrelated", b)
		}
	}
}

func TestControlRoundRepositoryScopedClaimsAndTelemetry(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "scoped-control", model.RoleAdmin)
	base := db.Control()
	first, e := base.StartRound(ctx, "fixture")
	must(t, e)
	second, e := base.StartRound(ctx, "fixture")
	must(t, e)
	for _, round := range []string{first, second} {
		must(t, db.transaction(ctx, func(tx pgx.Tx) error {
			return captureControl(model.WithControlRound(ctx, round), tx, u.ID, "ucampus", []model.ControlChange{{Operation: "created", Resource: "assignment", RecordID: "one", After: json.RawMessage(`{"title":"fixture"}`), OccurredAt: time.Now().UTC()}})
		}))
		must(t, base.FinishRound(ctx, round, nil, false))
	}
	must(t, base.Seal(ctx))
	_, e = base.Activate(ctx, first, "", "", false)
	must(t, e)
	_, e = base.Activate(ctx, second, "", "", false)
	must(t, e)
	progress, e := db.ControlRoundProgress(ctx, first)
	must(t, e)
	if progress.Total != 1 || progress.OtherQueued != 1 || progress.Preflight() == nil {
		t.Fatal(progress)
	}
	scoped, e := db.ControlRoundRepository(first)
	must(t, e)
	if _, ok := scoped.(model.ControlItemRepository); !ok {
		t.Fatal("lost item/artifact repository capability")
	}
	batch, e := scoped.Claim(ctx, "executor")
	must(t, e)
	if batch.RoundID != first {
		t.Fatal("claimed other round")
	}
	_, e = scoped.Claim(ctx, "executor")
	if e == nil {
		t.Fatal("claimed globally queued other round")
	}
	must(t, scoped.Heartbeat(ctx, "executor"))
	other, e := db.ControlRoundProgress(ctx, second)
	must(t, e)
	if other.Batches["queued"] != 1 || other.Batches["running"] != 0 {
		t.Fatal("changed unrelated queue", other)
	}
	batch.Status = "completed"
	must(t, scoped.SetBatch(ctx, batch))
	p, e := db.ControlRoundProgress(ctx, first)
	must(t, e)
	if !p.Done() {
		t.Fatal(p)
	}
}
