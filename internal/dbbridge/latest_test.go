package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"academic/backend/internal/model"
	"academic/backend/internal/trigger"
)

func latestRecord(content string, at time.Time) model.PlatformRecord {
	h := sha256.Sum256([]byte(content))
	return model.PlatformRecord{Source: "chaoxing", Kind: "course", ExternalID: "c", Hash: hex.EncodeToString(h[:]), Content: []byte(content), ObservedAt: at}
}

func TestPostgresLatestPlatformAndTriggers(t *testing.T) {
	db := testDB(t)
	ctx, stats := model.WithRefreshReport(context.Background())
	u := fixtureUser(t, db, "latest", model.RoleUser)
	r := db.Platforms()
	c, e := r.Create(ctx, u.ID, "chaoxing")
	must(t, e)
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	l, e := r.Acquire(ctx, scope)
	must(t, e)
	must(t, r.SaveAuth(ctx, l, []byte("cipher"), "active", time.Now().Add(time.Hour)))
	var changes []trigger.Change
	db.SetTriggerHandler(trigger.HandlerFunc(func(ctx context.Context, c trigger.Change) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("handler has no deadline")
		}
		rows, e := r.Records(ctx, scope, "course", 0, 10)
		must(t, e)
		if len(rows) != 1 {
			t.Error("handler ran before commit")
		}
		changes = append(changes, c)
		return errors.New("secret error must not be logged")
	}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	a := latestRecord(`{"title":"A"}`, at)
	b := latestRecord(`{"title":"B"}`, at.Add(time.Second))
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{a}, nil, nil))
	rows, e := r.Records(ctx, scope, "course", 0, 10)
	must(t, e)
	id := rows[0].ID
	noop := a
	noop.ObservedAt = at.Add(time.Hour)
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{noop}, nil, nil))
	rows, e = r.Records(ctx, scope, "course", 0, 10)
	must(t, e)
	if len(changes) != 1 || !rows[0].ObservedAt.Equal(at) {
		t.Fatal("noop rewrote record or emitted")
	}
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{b}, nil, nil))
	a.ObservedAt = at.Add(2 * time.Second)
	must(t, r.Commit(ctx, l, "course", []model.PlatformRecord{a}, nil, nil))
	rows, e = r.Records(ctx, scope, "course", 0, 10)
	must(t, e)
	if len(rows) != 1 || rows[0].ID != id || rows[0].Hash != a.Hash || !rows[0].ObservedAt.Equal(a.ObservedAt) {
		t.Fatal("A/B/A did not preserve latest stable ID", rows)
	}
	if len(changes) != 3 || changes[0].Operation != trigger.Created || changes[1].Operation != trigger.Updated || !reflect.DeepEqual(changes[1].ChangedFields, []string{"title"}) {
		t.Fatal("wrong changes", changes)
	}
	bad := a
	bad.ExternalID = "bad"
	bad.Hash = "invalid"
	if e = r.Commit(ctx, l, "course", []model.PlatformRecord{b, bad}, nil, nil); e == nil {
		t.Fatal("invalid commit succeeded")
	}
	rows, e = r.Records(ctx, scope, "course", 0, 10)
	must(t, e)
	if len(changes) != 3 || rows[0].Hash != a.Hash {
		t.Fatal("rollback emitted or persisted")
	}
	result := stats.Snapshot()[0]
	if result.Created != 1 || result.Updated != 2 || result.Unchanged != 1 {
		t.Fatalf("committed platform counts (rollback excluded): %+v", result)
	}
	db.SetTriggerHandler(nil)
	must(t, r.Release(ctx, l))
	must(t, r.Disconnect(ctx, scope))
	connections, e := r.List(ctx, u.ID)
	must(t, e)
	if len(connections) != 0 {
		t.Fatal("disconnected platform still listed")
	}
	rows, e = r.Records(ctx, scope, "course", 0, 10)
	must(t, e)
	if len(rows) != 1 {
		t.Fatal("disconnect deleted archived platform data")
	}
}

func TestPostgresLatestDWSAndAttachmentTriggers(t *testing.T) {
	db := testDB(t)
	ctx, stats := model.WithRefreshReport(context.Background())
	u := fixtureUser(t, db, "latest-dws", model.RoleUser)
	r := db.DWS()
	c, e := r.CreateConnection(ctx, model.DWSConnection{UserID: u.ID, Profile: "fixture"})
	must(t, e)
	s := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	must(t, r.SetGroups(ctx, s, []model.Group{{ID: "g", Name: "group"}}))
	l, e := r.AcquireLease(ctx, s, "g", time.Minute)
	must(t, e)
	var changes []trigger.Change
	db.SetTriggerHandler(trigger.HandlerFunc(func(ctx context.Context, c trigger.Change) error { changes = append(changes, c); return nil }))
	at := time.Now().UTC().Truncate(time.Microsecond)
	m := model.Message{GroupID: "g", ExternalID: "m", Content: []byte("A"), SourceVersion: "1", ObservedAt: at}
	setHash := func() { h := sha256.Sum256(m.Content); m.ContentHash = hex.EncodeToString(h[:]) }
	setHash()
	commit := func() {
		must(t, r.CommitSync(ctx, l, model.SyncCommit{Messages: []model.Message{m}, Checkpoint: model.Checkpoint{GroupID: "g", PollWatermark: at}}))
	}
	commit()
	rows, e := r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	id := rows[0].ID
	m.ObservedAt = at.Add(time.Hour)
	commit()
	rows, e = r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	if !rows[0].ObservedAt.Equal(at) {
		t.Fatal("noop updated observed_at")
	}
	for _, text := range []string{"B", "A"} {
		m.Content = []byte(text)
		m.SourceVersion = text
		setHash()
		commit()
	}
	rows, e = r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	if len(rows) != 1 || rows[0].ID != id || string(rows[0].Content) != "A" || len(changes) != 0 {
		t.Fatal("DWS latest/noevent failed")
	}
	result := stats.Snapshot()[0]
	if result.Created != 1 || result.Updated != 2 || result.Unchanged != 1 {
		t.Fatalf("committed DWS counts: %+v", result)
	}
	// A stale watermark fails after row writes; none of its counts may escape rollback.
	beforeStats := stats.Snapshot()[0]
	bad := m
	bad.ExternalID = "rolled-back"
	if err := r.CommitSync(ctx, l, model.SyncCommit{Messages: []model.Message{bad}, Checkpoint: model.Checkpoint{GroupID: "g", PollWatermark: at.Add(-time.Second)}}); err == nil {
		t.Fatal("stale checkpoint accepted")
	}
	if stats.Snapshot()[0] != beforeStats {
		t.Fatal("rolled back DWS writes counted")
	}
	a := model.Attachment{ID: "a", MessageID: id, Name: "a.txt", MediaType: "text/plain", Data: []byte("old")}
	put := func() {
		h := sha256.Sum256(a.Data)
		a.SHA256 = hex.EncodeToString(h[:])
		must(t, r.PutAttachment(ctx, l, a))
	}
	put()
	put()
	a.Data = []byte("new")
	a.Name = "new.txt"
	put()
	got, e := r.GetAttachment(ctx, s, id, "a")
	must(t, e)
	if string(got.Data) != "new" || got.Name != "new.txt" || len(changes) != 2 || changes[0].Operation != trigger.Created || changes[1].Operation != trigger.Updated || changes[1].Resource != "attachment" {
		t.Fatal("attachment latest/events failed", changes)
	}
	// Force failure after the attachment upsert to verify no after-commit event.
	_, e = db.pool.Exec(ctx, `CREATE FUNCTION expire_attachment_lease() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE dws_leases SET expires_at='epoch' WHERE user_id=NEW.user_id AND connection_id=NEW.connection_id; RETURN NEW; END $$; CREATE TRIGGER expire_attachment_lease AFTER UPDATE ON dws_attachments FOR EACH ROW EXECUTE FUNCTION expire_attachment_lease()`)
	must(t, e)
	a.Data = []byte("rolled back")
	h := sha256.Sum256(a.Data)
	a.SHA256 = hex.EncodeToString(h[:])
	if e = r.PutAttachment(ctx, l, a); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("expected attachment rollback", e)
	}
	got, e = r.GetAttachment(ctx, s, id, "a")
	must(t, e)
	if string(got.Data) != "new" || len(changes) != 2 {
		t.Fatal("attachment rollback persisted or emitted")
	}
	stale := l
	stale.Token++
	if e = r.PutAttachment(ctx, stale, a); e == nil {
		t.Fatal("stale lease accepted")
	}
	if len(changes) != 2 {
		t.Fatal("rollback emitted")
	}
	must(t, r.Disconnect(ctx, s))
	connections, e := r.ListConnections(ctx, u.ID)
	must(t, e)
	if len(connections) != 0 {
		t.Fatal("disconnected DWS still listed")
	}
	rows, e = r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	if len(rows) != 1 {
		t.Fatal("disconnect deleted archived DWS messages")
	}
}

func TestPostgresMigrationV3LatestAndAttachments(t *testing.T) {
	db := testDBVersion(t, 3)
	ctx := context.Background()
	u := fixtureUser(t, db, "migration", model.RoleUser)
	c, e := db.Platforms().Create(ctx, u.ID, "chaoxing")
	must(t, e)
	at := time.Now().UTC().Truncate(time.Microsecond)
	var platformWinner int64
	for i, observed := range []time.Time{at.Add(time.Second), at, at.Add(time.Second)} {
		var id int64
		must(t, db.pool.QueryRow(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,$2,'chaoxing','course','c','',$3,'{}',$4) RETURNING id`, u.ID, c.ID, string(rune('a'+i)), observed).Scan(&id))
		if i == 2 {
			platformWinner = id
		}
	}
	dc, e := db.DWS().CreateConnection(ctx, model.DWSConnection{UserID: u.ID, Profile: "fixture"})
	must(t, e)
	ids := []int64{}
	// Highest id is deliberately older, so observed_at must take precedence.
	for i, observed := range []time.Time{at, at.Add(time.Second), at.Add(time.Second), at.Add(-time.Second)} {
		var id int64
		must(t, db.pool.QueryRow(ctx, `INSERT INTO dws_messages(user_id,connection_id,group_id,external_id,source_version,content_hash,content,observed_at) VALUES($1,$2,'g','m',$3,'hash','content',$4) RETURNING id`, u.ID, dc.ID, string(rune('a'+i)), observed).Scan(&id))
		ids = append(ids, id)
	}
	for i, id := range ids {
		_, e = db.pool.Exec(ctx, `INSERT INTO dws_attachments(user_id,connection_id,message_id,id,name,media_type,sha256,data) VALUES($1,$2,$3,'shared',$4,'text/plain',$4,$5),($1,$2,$3,$4,$4,'text/plain',$4,$5)`, u.ID, dc.ID, id, string(rune('a'+i)), []byte{byte('a' + i)})
		must(t, e)
	}
	must(t, db.Migrate(ctx))
	must(t, db.Migrate(ctx))
	var id int64
	var n int
	must(t, db.pool.QueryRow(ctx, `SELECT id FROM platform_records`).Scan(&id))
	if id != platformWinner {
		t.Fatal("wrong platform winner")
	}
	must(t, db.pool.QueryRow(ctx, `SELECT id FROM dws_messages`).Scan(&id))
	if id != ids[2] {
		t.Fatal("wrong message winner")
	}
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM dws_attachments WHERE message_id=$1`, id).Scan(&n))
	if n != 5 {
		t.Fatal("lost distinct attachments", n)
	}
	a, e := db.DWS().GetAttachment(ctx, model.DWSScope{UserID: u.ID, ConnectionID: dc.ID}, id, "shared")
	must(t, e)
	if string(a.Data) != "c" {
		t.Fatal("collision did not prefer newest message", string(a.Data))
	}
	for _, key := range []string{"a", "b", "c", "d"} {
		a, e := db.DWS().GetAttachment(ctx, model.DWSScope{UserID: u.ID, ConnectionID: dc.ID}, id, key)
		must(t, e)
		if string(a.Data) != key {
			t.Fatal("attachment bytes lost")
		}
	}
}
