package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func replayFixture(t *testing.T) (*DB, string, time.Time) {
	t.Helper()
	db := testDB(t)
	u := fixtureUser(t, db, "replay-user", model.RoleUser)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	_, e := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,'active','chaoxing','active'),($1,'expired','mooc','expired')`, u.ID)
	must(t, e)
	for _, row := range []struct{ connection, source, kind, external, parent, content string }{{"active", "chaoxing", "course", `["course","class"]`, "", `{"title":"Calculus","external_id":"[\"course\",\"class\"]","fields":{"course_id":"course","class_id":"class"}}`}, {"active", "chaoxing", "assignment", "work", `["course","class"]`, `{"title":"Exercise","parent_id":"[\"course\",\"class\"]","content":"original body","source_content":"source retained","fields":{"status":"open"}}`}, {"expired", "mooc", "notice", "hidden", "", `{"title":"hidden"}`}} {
		_, e = db.pool.Exec(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at)VALUES($1,$2,$3,$4,$5,$6,'fixture',$7,$8)`, u.ID, row.connection, row.source, row.kind, row.external, row.parent, []byte(row.content), at)
		must(t, e)
	}
	_, e = db.pool.Exec(ctx, `INSERT INTO dws_connections(user_id,id,profile,mode,state) VALUES($1,'dws','fixture','personal','active')`, u.ID)
	must(t, e)
	_, e = db.pool.Exec(ctx, `INSERT INTO dws_groups(user_id,connection_id,id,name) VALUES($1,'dws','g1','Group 1'),($1,'dws','g2','Group 2')`, u.ID)
	must(t, e)
	for _, group := range []string{"g1", "g2", "unauthorized"} {
		_, e = db.pool.Exec(ctx, `INSERT INTO dws_messages(user_id,connection_id,group_id,external_id,source_version,content_hash,content,observed_at)VALUES($1,'dws',$2,$2||'-native','v','hash',convert_to(jsonb_build_object('conversationId',$2::text,'text','archive message')::text,'UTF8'),$3)`, u.ID, group, at)
		must(t, e)
	}
	_, e = db.pool.Exec(ctx, `INSERT INTO dws_attachments(user_id,connection_id,message_id,id,name,media_type,sha256,data)SELECT user_id,connection_id,id,'a','notes.pdf','application/pdf','fixture',decode('ff','hex') FROM dws_messages WHERE user_id=$1 AND group_id='g1'`, u.ID)
	must(t, e)
	return db, u.ID, at
}
func TestArchiveReplayDryRunAtomicParkAndIdempotency(t *testing.T) {
	db, owner, at := replayFixture(t)
	ctx := context.Background()
	old, e := db.Control().StartRound(ctx, "old")
	must(t, e)
	controlCourseEvents(t, db, old, owner, "dws", []model.ControlChange{{Operation: "created", Resource: "message", ConnectionID: "dws", GroupID: "g1", RecordID: "old", After: json.RawMessage(`{"conversationId":"g1","text":"old"}`)}})
	must(t, db.Control().FinishRound(ctx, old, nil, false))
	must(t, db.Control().Seal(ctx))
	_, e = db.Control().Activate(ctx, old, "", "", false)
	must(t, e)
	// A completed unrelated round still has unsealed events; replay may not seal it.
	untouched, e := db.Control().StartRound(ctx, "untouched")
	must(t, e)
	controlCourseEvents(t, db, untouched, owner, "dws", []model.ControlChange{{Resource: "message", ConnectionID: "dws", GroupID: "g1", RecordID: "untouched", After: json.RawMessage(`{"conversationId":"g1"}`)}})
	must(t, db.Control().FinishRound(ctx, untouched, nil, false))
	before, e := db.Control().GetBatch(ctx, controlCourseBatches(t, db, old)[0].ID)
	must(t, e)
	report, e := db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: "fixture"})
	must(t, e)
	if report.FreezeCount != 5 || len(report.Parked) != 1 || len(report.BatchIDs) != 0 {
		t.Fatal(report)
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_rounds WHERE id=$1`, report.RoundID).Scan(&count))
	if count != 0 {
		t.Fatal("dry run wrote a round")
	}
	_, e = db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: "fixture", Execute: true, Backup: func(context.Context, ArchiveReplayReport) error { return nil }})
	if e == nil {
		t.Fatal("queued batches silently absorbed")
	}
	backups := 0
	report, e = db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: "fixture", Execute: true, ParkQueued: true, Backup: func(_ context.Context, r ArchiveReplayReport) error {
		backups++
		if len(r.Parked) != 1 || len(r.Parked[0].EventIDs) != 1 || r.Parked[0].Batch.Status != "queued" {
			t.Fatal("incomplete backup", r)
		}
		return nil
	}})
	must(t, e)
	if backups != 1 || report.FreezeCount != 5 || len(report.BatchIDs) != 3 || report.Batches["pending"] != 3 {
		t.Fatal(report)
	}
	after, e := db.Control().GetBatch(ctx, before.ID)
	must(t, e)
	if after.Status != "pending" || after.Error != "ARCHIVE_REPLAY_PARKED_QUEUED" || string(after.Trigger) != string(before.Trigger) || string(after.Filter) != string(before.Filter) || len(after.Changes) != len(before.Changes) {
		t.Fatal("old history changed", after)
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE round_id=$1 AND batch_id IS NULL`, untouched).Scan(&count))
	if count != 1 {
		t.Fatal("other round sealed")
	}
	batches := controlCourseBatches(t, db, report.RoundID)
	total := 0
	groups := map[string]bool{}
	for _, b := range batches {
		total += b.Count
		if b.Status != "pending" || b.Scope == nil {
			t.Fatal(b)
		}
		if b.Source == "dws" {
			groups[b.Scope.GroupID] = true
		}
		for _, c := range b.Changes {
			if !c.OccurredAt.Equal(at) || c.Operation != "created" {
				t.Fatal("observation time changed", c)
			}
			var snap map[string]any
			must(t, json.Unmarshal(c.After, &snap))
			if snap["archive_replay"] == nil {
				t.Fatal("replay semantics missing")
			}
			if c.Resource == "assignment" && (snap["source_content"] != "source retained" || snap["content"] != "original body") {
				t.Fatal("source body changed", snap)
			}
		}
	}
	if total != 5 || !groups["g1"] || !groups["g2"] {
		t.Fatal("group/source count lost", batches)
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_items WHERE round_id=$1`, report.RoundID).Scan(&count))
	if count != 5 {
		t.Fatal("items not atomically seeded")
	}
	again, e := db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: "fixture", Execute: true, ParkQueued: true, Backup: func(context.Context, ArchiveReplayReport) error {
		t.Fatal("idempotent replay wrote backup")
		return nil
	}})
	must(t, e)
	if !again.Existing || again.RoundID != report.RoundID || again.Digest != report.Digest {
		t.Fatal(again)
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE round_id=$1`, report.RoundID).Scan(&count))
	if count != 5 {
		t.Fatal("idempotent retry duplicated events")
	}
}
func TestArchiveReplayRollbackAndRunningRefusal(t *testing.T) {
	db, owner, _ := replayFixture(t)
	ctx := context.Background()
	key := "rollback"
	_, e := db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: key, Execute: true, Backup: func(context.Context, ArchiveReplayReport) error { return errors.New("disk failure") }})
	if e == nil {
		t.Fatal("backup failure ignored")
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_rounds WHERE id=$1`, archiveReplayRound(key)).Scan(&count))
	if count != 0 {
		t.Fatal("failed backup leaked round")
	}
	old, e := db.Control().StartRound(ctx, "running")
	must(t, e)
	controlCourseEvents(t, db, old, owner, "dws", []model.ControlChange{{Resource: "message", ConnectionID: "dws", GroupID: "g1", RecordID: "run", After: json.RawMessage(`{"conversationId":"g1"}`)}})
	must(t, db.Control().FinishRound(ctx, old, nil, false))
	must(t, db.Control().Seal(ctx))
	_, e = db.Control().Activate(ctx, old, "", "", false)
	must(t, e)
	_, e = db.Control().Claim(ctx, "runner")
	must(t, e)
	_, e = db.PrepareArchiveReplay(ctx, ArchiveReplayOptions{Key: key, Execute: true, ParkQueued: true, Backup: func(context.Context, ArchiveReplayReport) error { return nil }})
	if e == nil {
		t.Fatal("running control accepted")
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_rounds WHERE id=$1`, archiveReplayRound(key)).Scan(&count))
	if count != 0 {
		t.Fatal("refused running leaked round")
	}
}
