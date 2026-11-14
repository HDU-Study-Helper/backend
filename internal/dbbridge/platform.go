package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"academic/backend/internal/model"
	"academic/backend/internal/trigger"
	"github.com/jackc/pgx/v5"
)

const schemaV2 = `
CREATE TABLE platform_connections (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,id text NOT NULL,source text NOT NULL CHECK(source IN ('chaoxing','mooc','ucampus')),
 state text NOT NULL DEFAULT 'pending',enabled boolean NOT NULL DEFAULT true,session bytea NOT NULL DEFAULT '',auth_expires_at timestamptz NOT NULL DEFAULT 'epoch',
 error_code text NOT NULL DEFAULT '',lease_token bigint NOT NULL DEFAULT 0,lease_until timestamptz NOT NULL DEFAULT 'epoch',updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,id),CHECK(octet_length(session)<=2097152)
);
CREATE SEQUENCE platform_fencing_seq;
CREATE TABLE platform_checkpoints(user_id text NOT NULL,connection_id text NOT NULL,kind text NOT NULL,cursor bytea NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,connection_id,kind),FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE,CHECK(octet_length(cursor)<=2097152));
CREATE TABLE platform_records(id bigserial PRIMARY KEY,user_id text NOT NULL,connection_id text NOT NULL,source text NOT NULL,kind text NOT NULL,external_id text NOT NULL,parent_id text NOT NULL,hash text NOT NULL,content jsonb NOT NULL,observed_at timestamptz NOT NULL,UNIQUE(user_id,connection_id,kind,external_id,parent_id,hash),FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE);
CREATE INDEX platform_records_page ON platform_records(user_id,connection_id,kind,id);
`

type platformRepo struct{ db *DB }

func (d *DB) Platforms() model.PlatformRepository { return &platformRepo{d} }
func validSource(s string) bool {
	return s == "chaoxing" || s == "mooc" || s == "ucampus" || s == "jwxt"
}
func scanPlatform(row pgx.Row) (c model.PlatformConnection, err error) {
	err = row.Scan(&c.ID, &c.UserID, &c.Source, &c.State, &c.Enabled, &c.ErrorCode, &c.UpdatedAt)
	return c, storageError(err)
}

const platformCols = "id,user_id,source,state,enabled,error_code,updated_at"

func (r *platformRepo) Create(ctx context.Context, owner, source string) (c model.PlatformConnection, err error) {
	if owner == "" || !validSource(source) {
		return c, model.ErrInvalidArgument
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&disabled); e != nil {
			return storageError(e)
		}
		if disabled {
			return model.ErrConflict
		}
		var e error
		c, e = scanPlatform(tx.QueryRow(ctx, `SELECT `+platformCols+` FROM platform_connections WHERE user_id=$1 AND source=$2 AND enabled AND state IN ('pending','scanned') ORDER BY updated_at LIMIT 1`, owner, source))
		if e == nil {
			return nil
		}
		if e != model.ErrNotFound {
			return e
		}
		c, e = scanPlatform(tx.QueryRow(ctx, `INSERT INTO platform_connections(user_id,id,source)VALUES($1,$2,$3) RETURNING `+platformCols, owner, newID(), source))
		return e
	})
	return
}
func (r *platformRepo) Get(ctx context.Context, s model.DWSScope) (model.PlatformConnection, error) {
	return scanPlatform(r.db.pool.QueryRow(ctx, `SELECT `+platformCols+` FROM platform_connections WHERE user_id=$1 AND id=$2`, s.UserID, s.ConnectionID))
}
func (r *platformRepo) List(ctx context.Context, owner string) ([]model.PlatformConnection, error) {
	return r.query(ctx, `SELECT `+platformCols+` FROM platform_connections WHERE user_id=$1 AND enabled ORDER BY id LIMIT 100`, owner)
}
func (r *platformRepo) Targets(ctx context.Context, after string, limit int) ([]model.PlatformConnection, error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidArgument
	}
	return r.query(ctx, `SELECT c.id,c.user_id,c.source,c.state,c.enabled,c.error_code,c.updated_at FROM platform_connections c JOIN users u ON u.id=c.user_id WHERE c.id>$1 AND c.enabled AND NOT u.disabled AND c.state='active' AND c.source<>'jwxt' ORDER BY c.id LIMIT $2`, after, limit)
}
func (r *platformRepo) query(ctx context.Context, q string, args ...any) ([]model.PlatformConnection, error) {
	rows, e := r.db.pool.Query(ctx, q, args...)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.PlatformConnection{}
	for rows.Next() {
		c, e := scanPlatform(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, storageError(rows.Err())
}
func (r *platformRepo) Acquire(ctx context.Context, s model.DWSScope) (l model.PlatformLease, err error) {
	l.Scope = s
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1 FOR UPDATE`, s.UserID).Scan(&disabled); e != nil {
			return storageError(e)
		}
		if disabled {
			return model.ErrLeaseLost
		}
		e := tx.QueryRow(ctx, `UPDATE platform_connections SET lease_token=nextval('platform_fencing_seq'),lease_until=clock_timestamp()+interval '90 seconds' WHERE user_id=$1 AND id=$2 AND enabled AND lease_until<=clock_timestamp() RETURNING lease_token`, s.UserID, s.ConnectionID).Scan(&l.Token)
		if e == pgx.ErrNoRows {
			return model.ErrConflict
		}
		return storageError(e)
	})
	return
}
func (r *platformRepo) Release(ctx context.Context, l model.PlatformLease) error {
	_, e := r.db.pool.Exec(ctx, `UPDATE platform_connections SET lease_until='epoch' WHERE user_id=$1 AND id=$2 AND lease_token=$3`, l.Scope.UserID, l.Scope.ConnectionID, l.Token)
	return storageError(e)
}
func platformLock(ctx context.Context, tx pgx.Tx, l model.PlatformLease) error {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT c.enabled AND NOT u.disabled AND c.lease_token=$3 AND c.lease_until>clock_timestamp() FROM platform_connections c JOIN users u ON u.id=c.user_id WHERE c.user_id=$1 AND c.id=$2 FOR UPDATE OF u,c`, l.Scope.UserID, l.Scope.ConnectionID, l.Token).Scan(&ok)
	if e != nil {
		return storageError(e)
	}
	if !ok {
		return model.ErrLeaseLost
	}
	return nil
}
func (r *platformRepo) Load(ctx context.Context, l model.PlatformLease, kind string) (out model.PlatformState, err error) {
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		var e error
		out.Connection, e = scanPlatform(tx.QueryRow(ctx, `SELECT `+platformCols+` FROM platform_connections WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID))
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT session,auth_expires_at FROM platform_connections WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID).Scan(&out.Session, &out.AuthExpiresAt); e != nil {
			return storageError(e)
		}
		e = tx.QueryRow(ctx, `SELECT cursor FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2 AND kind=$3`, l.Scope.UserID, l.Scope.ConnectionID, kind).Scan(&out.Cursor)
		if e == pgx.ErrNoRows {
			return nil
		}
		return storageError(e)
	})
	return
}
func (r *platformRepo) SaveAuth(ctx context.Context, l model.PlatformLease, session []byte, state string, expires time.Time) error {
	if len(session) > 2<<20 || (state != "pending" && state != "scanned" && state != "active" && state != "expired") {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		var previous string
		if e := tx.QueryRow(ctx, `SELECT state FROM platform_connections WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID).Scan(&previous); e != nil {
			return storageError(e)
		}
		if previous == "active" && (state == "pending" || state == "scanned") {
			return model.ErrConflict
		}
		_, e := tx.Exec(ctx, `UPDATE platform_connections SET session=$4,state=$5,auth_expires_at=$6,error_code='',updated_at=clock_timestamp() WHERE user_id=$1 AND id=$2 AND lease_token=$3`, l.Scope.UserID, l.Scope.ConnectionID, l.Token, nonNil(session), state, expires)
		if e != nil {
			return storageError(e)
		}
		if state == "pending" {
			_, e = tx.Exec(ctx, `DELETE FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2`, l.Scope.UserID, l.Scope.ConnectionID)
		}
		return storageError(e)
	})
}
func (r *platformRepo) Commit(ctx context.Context, l model.PlatformLease, kind string, records []model.PlatformRecord, session, cursor []byte) error {
	return r.commit(ctx, l, kind, records, session, cursor, nil, nil)
}
func (r *platformRepo) CommitTimetable(ctx context.Context, l model.PlatformLease, records []model.PlatformRecord, session []byte, b model.TimetableRefresh) (model.TimetableResult, error) {
	var result model.TimetableResult
	err := r.commit(ctx, l, "schedule", records, session, nil, &b, &result)
	result.Records = len(records)
	return result, err
}
func (r *platformRepo) commit(ctx context.Context, l model.PlatformLease, kind string, records []model.PlatformRecord, session, cursor []byte, full *model.TimetableRefresh, result *model.TimetableResult) error {
	maxRecords := 500
	if full != nil {
		maxRecords = 5000
	}
	if len(records) > maxRecords || len(session) > 2<<20 || len(cursor) > 2<<20 {
		return model.ErrInvalidArgument
	}
	total := 0
	for _, v := range records {
		total += len(v.Content)
		if (v.Kind != kind && !(kind == "discussion" && v.Source == "mooc" && v.Kind == "discussion_reply_user")) || v.ExternalID == "" || !json.Valid(v.Content) || len(v.Content) > 1<<20 || !validSource(v.Source) {
			return model.ErrInvalidArgument
		}
	}
	if total > 8<<20 {
		return model.ErrInvalidArgument
	}
	var changes []trigger.Change
	var snapshots []model.ControlChange
	sourceName := ""
	created, updated, unchanged := 0, 0, 0
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		var source, state string
		if e := tx.QueryRow(ctx, `SELECT source,state FROM platform_connections WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID).Scan(&source, &state); e != nil {
			return storageError(e)
		}
		sourceName = source
		if state != "active" {
			return model.ErrConflict
		}
		var filterErr error
		records, filterErr = filterExcludedPlatformRecords(ctx, tx, l.Scope.UserID, l.Scope.ConnectionID, source, records)
		if filterErr != nil {
			return filterErr
		}
		if full != nil {
			if source != "jwxt" || kind != "schedule" {
				return model.ErrInvalidArgument
			}
			parentBytes, _ := json.Marshal([]string{strconv.Itoa(full.AcademicYear), strconv.Itoa(full.Term)})
			ids := []string{}
			for _, v := range records {
				if v.ParentID != string(parentBytes) {
					return model.ErrInvalidArgument
				}
				ids = append(ids, v.ExternalID)
			}
			if _, e := tx.Exec(ctx, `DELETE FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='schedule' AND parent_id=$3 AND NOT(external_id=ANY($4::text[]))`, l.Scope.UserID, l.Scope.ConnectionID, string(parentBytes), ids); e != nil {
				return storageError(e)
			}
		}
		for _, v := range records {
			if v.Source != source {
				return model.ErrInvalidArgument
			}
			h := sha256.Sum256(v.Content)
			if v.Hash != hex.EncodeToString(h[:]) {
				return model.ErrInvalidArgument
			}
			var before []byte
			var previousHash string
			e := tx.QueryRow(ctx, `SELECT hash,content FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind=$3 AND external_id=$4 AND parent_id=$5`, l.Scope.UserID, l.Scope.ConnectionID, v.Kind, v.ExternalID, v.ParentID).Scan(&previousHash, &before)
			if e != nil && e != pgx.ErrNoRows {
				return storageError(e)
			}
			if e == nil && previousHash == v.Hash {
				unchanged++
				continue
			}
			operation := trigger.Updated
			if e == pgx.ErrNoRows {
				operation = trigger.Created
				created++
			}
			if operation == trigger.Updated {
				updated++
			}
			var id int64
			e = tx.QueryRow(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(user_id,connection_id,kind,external_id,parent_id) DO UPDATE SET hash=EXCLUDED.hash,content=EXCLUDED.content,observed_at=EXCLUDED.observed_at WHERE platform_records.hash<>EXCLUDED.hash RETURNING id`, l.Scope.UserID, l.Scope.ConnectionID, source, v.Kind, v.ExternalID, v.ParentID, v.Hash, []byte(v.Content), v.ObservedAt).Scan(&id)
			if e != nil {
				return storageError(e)
			}
			// Roster rows are evidence for their parent topic, not independent work items.
			// The root discussion snapshot carries roster counts and coverage when the
			// bounded list read finishes; retain every row for authorized queries.
			if source == "mooc" && v.Kind == "discussion_reply_user" {
				continue
			}
			snapshots = append(snapshots, model.ControlChange{Operation: string(operation), Resource: v.Kind, ConnectionID: l.Scope.ConnectionID, RecordID: strconv.FormatInt(id, 10), Before: before, After: v.Content, ChangedFields: trigger.FieldChanges(before, v.Content), OccurredAt: v.ObservedAt})
			changes = append(changes, trigger.Change{UserID: l.Scope.UserID, ConnectionID: l.Scope.ConnectionID, Source: source, Resource: v.Kind, RecordID: strconv.FormatInt(id, 10), ExternalID: v.ExternalID, ParentID: v.ParentID, Operation: operation, ChangedFields: trigger.FieldChanges(before, v.Content), OccurredAt: v.ObservedAt})
		}
		if e := syncAcademicConnection(ctx, tx, l.Scope.UserID, l.Scope.ConnectionID); e != nil {
			return e
		}
		if e := captureControl(ctx, tx, l.Scope.UserID, source, snapshots); e != nil {
			return e
		}
		if full != nil {
			refreshed, e := refreshTimetable(ctx, tx, l.Scope.UserID, *full)
			if e != nil {
				return e
			}
			*result = refreshed
		}
		if _, e := tx.Exec(ctx, `UPDATE platform_connections SET session=$3,error_code='',updated_at=clock_timestamp() WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID, nonNil(session)); e != nil {
			return storageError(e)
		}
		_, e := tx.Exec(ctx, `INSERT INTO platform_checkpoints(user_id,connection_id,kind,cursor)VALUES($1,$2,$3,$4)ON CONFLICT(user_id,connection_id,kind)DO UPDATE SET cursor=EXCLUDED.cursor,updated_at=clock_timestamp()`, l.Scope.UserID, l.Scope.ConnectionID, checkpointKind(l, kind), nonNil(cursor))
		return storageError(e)
	})
	if err == nil {
		model.RefreshCommitted(ctx, sourceName, created, updated, unchanged)
		r.db.emit(ctx, changes)
	}
	return err
}
func (r *platformRepo) Fail(ctx context.Context, l model.PlatformLease, code string) error {
	switch code {
	case "auth_required", "parse_failed", "rate_limited", "verification_required", "unsupported", "upstream_unavailable", "invalid_argument":
	default:
		code = "internal"
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE platform_connections SET error_code=$3,state=CASE WHEN $3='auth_required' THEN 'expired' ELSE state END,updated_at=clock_timestamp() WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID, code)
		return storageError(e)
	})
}
func (r *platformRepo) Disconnect(ctx context.Context, s model.DWSScope) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE platform_connections SET enabled=false,state='disabled',session='',lease_token=nextval('platform_fencing_seq'),lease_until='epoch',updated_at=clock_timestamp()WHERE user_id=$1 AND id=$2`, s.UserID, s.ConnectionID)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() != 1 {
			return model.ErrNotFound
		}
		_, e = tx.Exec(ctx, `DELETE FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2`, s.UserID, s.ConnectionID)
		return storageError(e)
	})
}
func (r *platformRepo) Records(ctx context.Context, s model.DWSScope, kind string, after int64, limit int) ([]model.PlatformRecord, error) {
	if limit < 1 || limit > 101 || after < 0 {
		return nil, model.ErrInvalidArgument
	}
	if _, e := r.Get(ctx, s); e != nil {
		return nil, e
	}
	rows, e := r.db.pool.Query(ctx, `SELECT id,source,kind,external_id,parent_id,hash,content,observed_at FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND ($3='' OR kind=$3) AND id>$4 ORDER BY id LIMIT $5`, s.UserID, s.ConnectionID, kind, after, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.PlatformRecord{}
	for rows.Next() {
		var v model.PlatformRecord
		if e = rows.Scan(&v.ID, &v.Source, &v.Kind, &v.ExternalID, &v.ParentID, &v.Hash, &v.Content, &v.ObservedAt); e != nil {
			return nil, storageError(e)
		}
		out = append(out, v)
	}
	return out, storageError(rows.Err())
}

var _ model.PlatformRepository = (*platformRepo)(nil)
