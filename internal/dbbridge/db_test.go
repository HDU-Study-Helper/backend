package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenConfiguration(t *testing.T) {
	for _, tc := range []struct{ url, want string }{{"", "DATABASE_URL is required"}, {"postgres://%invalid", "invalid DATABASE_URL"}} {
		_, e := Open(context.Background(), tc.url)
		if e == nil || e.Error() != tc.want {
			t.Fatalf("configuration error: %v", e)
		}
	}
}

// This helper never reads DATABASE_URL. TEST_DATABASE_URL must designate a
// dedicated test database; every test uses and removes its own random schema.
func testDB(t *testing.T) *DB { return testDBVersion(t, 15) }

func testDBVersion(t *testing.T, version int) *DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated temporary PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	admin, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal("test pool failed")
	}
	schema := "test_dbbridge_" + newID()
	if _, e = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); e != nil {
		admin.Close()
		t.Fatal("create isolated test schema failed")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal("isolated pool failed")
	}
	db := &DB{pool: pool}
	t.Cleanup(func() {
		db.Close()
		cleanup, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if _, e := admin.Exec(cleanup, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); e != nil {
			t.Error("test schema cleanup failed")
		}
		admin.Close()
	})
	if version == 3 {
		_, e := db.pool.Exec(ctx, `CREATE TABLE dbbridge_migrations(version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());`+schemaV1+schemaV2+schemaV3+`INSERT INTO dbbridge_migrations(version) VALUES(1),(2),(3);`)
		must(t, e)
	} else if version == 8 {
		_, e := db.pool.Exec(ctx, `CREATE TABLE dbbridge_migrations(version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());`+schemaV1+schemaV2+schemaV3+schemaV4+schemaV5+schemaV6+schemaV7+schemaV8+`INSERT INTO dbbridge_migrations(version) VALUES(1),(2),(3),(4),(5),(6),(7),(8);`)
		must(t, e)
	} else {
		must(t, db.Migrate(ctx))
	}
	return db
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func fixtureUser(t *testing.T, db *DB, name string, role model.Role) model.User {
	t.Helper()
	u, e := db.Users().Create(context.Background(), model.User{Username: name, DisplayName: name, PasswordHash: "fixture-password-hash", Role: role})
	must(t, e)
	return u
}
func TestPostgresMigrationAndUsers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	must(t, db.Migrate(ctx))
	var n int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM dbbridge_migrations`).Scan(&n))
	if n != 15 {
		t.Fatal("migration not idempotent")
	}
	u, created, e := db.Users().BootstrapAdmin(ctx, model.User{Username: "admin", DisplayName: "Admin", PasswordHash: "fixture-hash"})
	must(t, e)
	if !created {
		t.Fatal("bootstrap did not create")
	}
	_, created, e = db.Users().BootstrapAdmin(ctx, model.User{Username: "other", PasswordHash: "fixture-hash"})
	must(t, e)
	if created {
		t.Fatal("bootstrap created twice")
	}
	role := model.RoleUser
	disabled := true
	for _, change := range []model.UserUpdate{{Role: &role}, {Disabled: &disabled}} {
		if _, e := db.Users().Update(ctx, u.ID, change); !errors.Is(e, model.ErrLastAdmin) {
			t.Fatalf("last admin update: %v", e)
		}
	}
	if e := db.Users().Delete(ctx, u.ID); !errors.Is(e, model.ErrLastAdmin) {
		t.Fatalf("last admin delete: %v", e)
	}
	hash := sha256.Sum256([]byte("fixture-session-token"))
	must(t, db.Users().CreateSession(ctx, model.Session{Hash: hash[:], UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}))
	sessionUser, e := db.Users().GetSessionUser(ctx, hash[:])
	must(t, e)
	if sessionUser.ID != u.ID {
		t.Fatal("wrong session owner")
	}
	password := "changed-fixture-hash"
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{PasswordHash: &password})
	must(t, e)
	if _, e := db.Users().GetSessionUser(ctx, hash[:]); e == nil {
		t.Fatal("password update did not revoke session")
	}
	ordinary := fixtureUser(t, db, "ordinary", model.RoleUser)
	must(t, db.Users().CreateSession(ctx, model.Session{Hash: hash[:], UserID: ordinary.ID, ExpiresAt: time.Now().Add(time.Hour)}))
	_, e = db.Users().Update(ctx, ordinary.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	if _, e := db.Users().GetSessionUser(ctx, hash[:]); e == nil {
		t.Fatal("disabled user session remained")
	}
}
func TestPostgresCompleteLinkAtomicIdentity(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "link-owner", model.RoleUser)
	other := fixtureUser(t, db, "link-other", model.RoleUser)
	r := db.DWS()
	c, e := r.CreatePendingConnection(ctx, u.ID)
	must(t, e)
	reused, e := r.CreatePendingConnection(ctx, u.ID)
	must(t, e)
	if c.ID != reused.ID || c.Profile != "" {
		t.Fatal("empty pending was not reused")
	}
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c.ID}
	lease, e := r.AcquireLease(ctx, scope, "", time.Minute)
	must(t, e)
	identity := model.DWSIdentity{Profile: "corp:user", CorpID: "corp", UpstreamUserID: "user"}
	workspace := []byte("synthetic-encrypted-workspace")
	for _, mode := range []string{"missing_corp", "missing_user", "foreign_owner", "stale_lease", "atomic_rollback"} {
		t.Run(mode, func(t *testing.T) {
			id, l := identity, lease
			switch mode {
			case "missing_corp":
				id.CorpID = ""
			case "missing_user":
				id.UpstreamUserID = ""
			case "foreign_owner":
				l.Scope.UserID = other.ID
			case "stale_lease":
				l.Token++
			case "atomic_rollback":
				_, e = db.pool.Exec(ctx, `CREATE FUNCTION reject_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rollback'; END $$; CREATE TRIGGER reject_link BEFORE UPDATE ON dws_connections FOR EACH ROW EXECUTE FUNCTION reject_link()`)
				must(t, e)
			}
			if e = r.CompleteLink(ctx, l, id, workspace); e == nil {
				t.Fatal("invalid completion succeeded")
			}
			if mode == "atomic_rollback" {
				_, e = db.pool.Exec(ctx, `DROP TRIGGER reject_link ON dws_connections; DROP FUNCTION reject_link()`)
				must(t, e)
			}
			unchanged, e := r.GetConnection(ctx, scope)
			must(t, e)
			b, e := r.LoadWorkspace(ctx, scope)
			must(t, e)
			if unchanged.Profile != "" || unchanged.CorpID != "" || unchanged.UpstreamUserID != "" || unchanged.State != "pending" || len(b) != 0 {
				t.Fatal("failed completion persisted partial state")
			}
		})
	}
	must(t, r.CompleteLink(ctx, lease, identity, workspace))
	bound, e := r.GetConnection(ctx, scope)
	must(t, e)
	if bound.Profile != identity.Profile || bound.State != "active" {
		t.Fatal("official identity not activated")
	}
	changed := model.DWSIdentity{Profile: "corp:other", CorpID: "corp", UpstreamUserID: "other"}
	if e = r.CompleteLink(ctx, lease, changed, []byte("replacement")); !errors.Is(e, model.ErrConflict) {
		t.Fatal("bound identity changed", e)
	}
	b, e := r.LoadWorkspace(ctx, scope)
	must(t, e)
	if string(b) != string(workspace) {
		t.Fatal("identity mismatch changed credentials")
	}
	must(t, r.CompleteLink(ctx, lease, identity, workspace))
	disabled := true
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	if e = r.CompleteLink(ctx, lease, identity, workspace); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("disabled owner completion allowed", e)
	}
}

func TestPostgresDWSAtomicScopeFencing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "owner", model.RoleUser)
	v := fixtureUser(t, db, "other", model.RoleUser)
	r := db.DWS()
	conn, e := r.CreateConnection(ctx, model.DWSConnection{UserID: u.ID, Profile: "fixture", Mode: model.DWSModePersonal})
	must(t, e)
	s := model.DWSScope{UserID: u.ID, ConnectionID: conn.ID}
	foreign := model.DWSScope{UserID: v.ID, ConnectionID: conn.ID}
	if _, e = r.GetConnection(ctx, foreign); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-owner connection exposed")
	}
	if _, e = r.AcquireLease(ctx, foreign, "", time.Minute); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-owner lease allowed")
	}
	auth, e := r.AcquireLease(ctx, s, "", time.Minute)
	must(t, e)
	must(t, r.SaveWorkspace(ctx, auth, []byte("encrypted-workspace")))
	must(t, r.ReleaseLease(ctx, auth))
	must(t, r.SetGroups(ctx, s, []model.Group{{ID: "g", Name: "Fixture"}, {ID: "g2", Name: "Other"}}))
	l, e := r.AcquireLease(ctx, s, "g", time.Minute)
	must(t, e)
	if _, e := r.AcquireLease(ctx, s, "g2", time.Minute); !errors.Is(e, model.ErrConflict) {
		t.Fatal("parallel connection lease allowed")
	}
	content := []byte(`{"text":"synthetic fixture"}`)
	h := sha256.Sum256(content)
	m := model.Message{GroupID: "g", ExternalID: "m", ContentHash: hex.EncodeToString(h[:]), Content: content, ObservedAt: time.Now()}
	cp := model.Checkpoint{GroupID: "g", CursorCiphertext: []byte("encrypted-cursor"), CLITokenCiphertext: []byte("encrypted-cli-token"), PollWatermark: time.Now().UTC()}
	c := model.SyncCommit{Messages: []model.Message{m}, Checkpoint: cp}
	must(t, r.CommitSync(ctx, l, c))
	must(t, r.CommitSync(ctx, l, c))
	msgs, e := r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	if len(msgs) != 1 {
		t.Fatal("version dedup failed")
	}
	m.SourceVersion = "v2"
	c.Messages = []model.Message{m}
	must(t, r.CommitSync(ctx, l, c))
	msgs, e = r.ListMessages(ctx, s, "g", 0, 1)
	must(t, e)
	if len(msgs) != 1 {
		t.Fatal("pagination limit failed")
	}
	next, e := r.ListMessages(ctx, s, "g", msgs[0].ID, 100)
	must(t, e)
	if len(next) != 0 || msgs[0].SourceVersion != "v2" {
		t.Fatal("stable message ID or latest version failed")
	}
	if _, e = r.ListMessages(ctx, foreign, "g", 0, 100); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner messages exposed")
	}
	if _, e = r.GetCheckpoint(ctx, foreign, "g"); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner checkpoint exposed")
	}
	aBytes := []byte("synthetic attachment")
	ah := sha256.Sum256(aBytes)
	a := model.Attachment{ID: "a", MessageID: msgs[0].ID, Name: "fixture.txt", MediaType: "text/plain", SHA256: hex.EncodeToString(ah[:]), Data: aBytes}
	must(t, r.PutAttachment(ctx, l, a))
	if _, e = r.GetAttachment(ctx, foreign, a.MessageID, a.ID); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross owner attachment exposed")
	}
	// A later invalid record rolls back an earlier insert and the checkpoint.
	m.ExternalID = "rolled-back"
	bad := m
	bad.ContentHash = "invalid"
	c.Messages = []model.Message{m, bad}
	c.Checkpoint.PollWatermark = cp.PollWatermark.Add(time.Hour)
	if e = r.CommitSync(ctx, l, c); !errors.Is(e, model.ErrInvalidArgument) {
		t.Fatalf("expected rollback: %v", e)
	}
	all, e := r.ListMessages(ctx, s, "g", 0, 100)
	must(t, e)
	if len(all) != 1 {
		t.Fatal("partial commit survived")
	}
	saved, e := r.GetCheckpoint(ctx, s, "g")
	must(t, e)
	if !saved.PollWatermark.Equal(cp.PollWatermark.Truncate(time.Microsecond)) {
		t.Fatal("failed sync advanced checkpoint")
	}
	job, e := r.CreateJob(ctx, s, "g")
	must(t, e)
	must(t, r.FailJob(ctx, s, job.ID, "upstream_unavailable"))
	saved2, e := r.GetCheckpoint(ctx, s, "g")
	must(t, e)
	if !saved2.PollWatermark.Equal(saved.PollWatermark) {
		t.Fatal("failed job advanced watermark")
	}
	// Revoking and restoring a group must never revive its old fencing token.
	must(t, r.SetGroups(ctx, s, []model.Group{{ID: "g2", Name: "Other"}}))
	if e = r.CommitSync(ctx, l, model.SyncCommit{Checkpoint: cp}); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatalf("revoked group lease committed: %v", e)
	}
	must(t, r.SetGroups(ctx, s, []model.Group{{ID: "g", Name: "Fixture"}}))
	if e = r.CommitSync(ctx, l, model.SyncCommit{Checkpoint: cp}); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatalf("restored group revived lease: %v", e)
	}
	current, e := r.AcquireLease(ctx, s, "g", time.Minute)
	must(t, e)
	pending, e := r.CreateJob(ctx, s, "g")
	must(t, e)
	must(t, r.StartJob(ctx, current, pending.ID))
	if e = r.FailJob(ctx, s, pending.ID, "internal"); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("unfenced failure changed running job")
	}
	if e = r.FailLeasedJob(ctx, l, pending.ID, "internal"); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("stale worker failed current job")
	}
	disabled := true
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	disabled = false
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: &disabled})
	must(t, e)
	if e = r.CommitSync(ctx, current, model.SyncCommit{Checkpoint: cp}); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("reenabling user revived old lease")
	}
	must(t, r.Disconnect(ctx, s))
	c.Messages = nil
	c.Checkpoint = cp
	if e = r.CommitSync(ctx, l, c); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatalf("disconnected worker committed: %v", e)
	}
	if e = r.SaveWorkspace(ctx, l, []byte("revived")); !errors.Is(e, model.ErrLeaseLost) {
		t.Fatal("disconnected workspace revived")
	}
}
