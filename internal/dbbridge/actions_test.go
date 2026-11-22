package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestV18ActionPersistence(t *testing.T) {
	ctx := context.Background()
	db := testDBVersion(t, 17)
	u := fixtureUser(t, db, "archive-owner", model.RoleUser)
	v := fixtureUser(t, db, "unrelated", model.RoleUser)
	must(t, db.Migrate(ctx))
	must(t, db.Migrate(ctx))
	a, e := (f.Registry{Repository: db.Agents()}).Create(ctx, u.ID, definition("worker", "mock", nil, nil))
	must(t, e)
	s := model.ActionScope{OwnerID: u.ID, AgentID: a.ID, RunID: newID()}
	must(t, db.Agents().SaveRun(ctx, f.RunRecord{OwnerID: u.ID, AgentID: a.ID, ID: s.RunID, Status: "running", StartedAt: time.Now()}))
	r := db.Actions()
	in := model.ActionInput{Key: "change1", Target: "calculus-final", Title: "Study", Evidence: []string{"event:123"}}
	one, e := r.PutAction(ctx, s, "todo", in)
	must(t, e)
	changed := in
	due := time.Now().UTC().Truncate(time.Second)
	changed.Due = &due
	_, e = r.PutAction(ctx, s, "todo", changed)
	if !errors.Is(e, model.ErrConflict) {
		t.Fatalf("reused key: %v", e)
	}
	changed.Key = "change2"
	two, e := r.PutAction(ctx, s, "todo", changed)
	must(t, e)
	replay, e := r.PutAction(ctx, s, "todo", in)
	must(t, e)
	if two.ID != one.ID || replay.Input.Key != changed.Key {
		t.Fatal("stale replay changed todo")
	}
	partial := in
	partial.Key = "change3"
	partial.Title = "Revised"
	newer, e := r.PutAction(ctx, s, "todo", partial)
	must(t, e)
	if newer.Input.Due == nil || !newer.Input.Due.Equal(due) {
		t.Fatal("partial revision erased deadline")
	}
	stolen := s
	stolen.OwnerID = v.ID
	_, e = r.ChangeAction(ctx, stolen, "todo", one.ID)
	if !errors.Is(e, model.ErrNotFound) {
		t.Fatal(e)
	}
	rows, e := r.ListActions(ctx, v.ID, "todo", 10, 0)
	must(t, e)
	if len(rows) != 0 {
		t.Fatal("owner leak")
	}
	job := model.ActionInput{Key: "remind", Title: "Exam", Body: "Study", Evidence: in.Evidence, Due: &due, Mode: "active", Reason: "Due today", Kind: "Notice", RepeatWeekly: true, Timezone: "Asia/Shanghai"}
	job.IntentQuote = "Please remind me weekly"
	job.IntentTurn = "turn"
	job.PreferenceID = "pref"
	job.ReminderKey = "exam-weekly"
	_, e = r.PutAction(ctx, s, "job", job)
	if !errors.Is(e, model.ErrInvalidArgument) {
		t.Fatal("unverified active notice", e)
	}
	_, e = db.pool.Exec(ctx, `INSERT INTO chat_threads(owner_id,id) VALUES($1,'thread')`, u.ID)
	must(t, e)
	_, e = db.pool.Exec(ctx, `INSERT INTO chat_turns(owner_id,thread_id,id,request_id,kind,message,state) VALUES($1,'thread','turn','req','message',$2,'completed')`, u.ID, job.IntentQuote)
	must(t, e)
	badQuote := job
	badQuote.IntentQuote = "Never said"
	_, e = r.PutAction(ctx, s, "job", badQuote)
	if !errors.Is(e, model.ErrInvalidArgument) {
		t.Fatal("forged user quotation", e)
	}
	_, e = db.pool.Exec(ctx, `INSERT INTO memories(memory_id,owner_user_id,visibility,memory_type,is_personal,level,title,content,source_refs) VALUES('pref',$1,'PUB','BASE',true,'L1','weekly','notice','["chat_turn:turn"]')`, u.ID)
	must(t, e)
	firstJob, e := r.PutAction(ctx, s, "job", job)
	must(t, e)
	job.Key = "other-channel"
	job.Reason = "alternate wording"
	sameJob, e := r.PutAction(ctx, s, "job", job)
	must(t, e)
	if sameJob.ID != firstJob.ID {
		t.Fatal("duplicate canonical reminder")
	}
	j, e := r.ClaimAction(ctx)
	must(t, e)
	_, e = r.ClaimAction(ctx)
	if !errors.Is(e, model.ErrNotFound) {
		t.Fatal("double claim", e)
	}
	must(t, r.FinishAction(ctx, j, "succeeded", ""))
	if e = r.FinishAction(ctx, j, "succeeded", ""); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("double fire", e)
	}
	notices, e := r.ListActions(ctx, u.ID, "notice", 10, 0)
	must(t, e)
	if len(notices) != 1 || notices[0].Status != "recorded" || notices[0].Input.Mode != "active" {
		t.Fatal(notices)
	}
	jobs, e := r.ListActions(ctx, u.ID, "job", 10, 0)
	must(t, e)
	if len(jobs) != 2 {
		t.Fatal("missing weekly successor", jobs)
	}
	_, e = r.ClaimAction(ctx)
	if !errors.Is(e, model.ErrNotFound) {
		t.Fatal("future job claimed", e)
	}
	_, e = db.pool.Exec(ctx, `UPDATE memories SET status='archived' WHERE owner_user_id=$1 AND memory_id='pref'`, u.ID)
	must(t, e)
	_, e = db.pool.Exec(ctx, `UPDATE public_actions SET due_at=now() WHERE owner_id=$1 AND kind='job' AND status='pending'`, u.ID)
	must(t, e)
	next, e := r.ClaimAction(ctx)
	must(t, e)
	must(t, r.FinishAction(ctx, next, "succeeded", ""))
	stopped, e := r.GetAction(ctx, u.ID, "job", next.ID)
	must(t, e)
	if stopped.Status != "cancelled" {
		t.Fatal("archived intent emitted a notice", stopped)
	}
	rollback := errors.New("rollback")
	if e = db.transaction(ctx, func(tx pgx.Tx) error {
		_, e := RecordQuestionNotice(ctx, tx, s, "a", "Confirm?")
		if e != nil {
			return e
		}
		return rollback
	}); !errors.Is(e, rollback) {
		t.Fatal(e)
	}
	notes, e := r.ListActions(ctx, u.ID, "notice", 10, 0)
	must(t, e)
	if len(notes) != 1 {
		t.Fatal("escaped question transaction")
	}
}
func TestWeeklyClockSkipsGapAndBacklog(t *testing.T) {
	first, _ := time.Parse(time.RFC3339, "2026-03-01T02:30:00-05:00")
	in := model.ActionInput{Due: &first, Timezone: "America/New_York"}
	next, e := nextWeekly(in, first)
	must(t, e)
	if next.Format(time.RFC3339) != "2026-03-15T06:30:00Z" {
		t.Fatal(next)
	}
	now, _ := time.Parse(time.RFC3339, "2026-06-22T00:00:00Z")
	next, e = nextWeekly(in, now)
	must(t, e)
	if next.Format(time.RFC3339) != "2026-06-28T06:30:00Z" {
		t.Fatal(next)
	}
}
