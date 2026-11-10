package dbbridge

import (
	"context"
	"encoding/json"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

const schemaV3 = `CREATE TABLE platform_sync_status(
 user_id text NOT NULL,connection_id text NOT NULL,kind text NOT NULL,
 lease_token bigint NOT NULL,last_attempt_at timestamptz NOT NULL,last_finished_at timestamptz,
 last_success_at timestamptz,last_error_code text NOT NULL DEFAULT '',last_duration_ms bigint,
 last_page_count integer,has_more boolean,in_progress boolean NOT NULL DEFAULT false,
 PRIMARY KEY(user_id,connection_id,kind),FOREIGN KEY(user_id,connection_id)REFERENCES platform_connections(user_id,id)ON DELETE CASCADE
);`

type previewRepo struct{ db *DB }

func (d *DB) Preview() model.PreviewRepository { return &previewRepo{d} }
func validKind(k string) bool {
	return k == "course" || k == "chapter" || k == "lesson" || k == "schedule" || k == "assignment" || k == "exam" || k == "progress" || k == "notice" || k == "discussion" || k == "discussion_reply_user" || k == "material"
}
func (r *platformRepo) StartSync(ctx context.Context, l model.PlatformLease, kind string) error {
	if !validKind(kind) {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO platform_sync_status(user_id,connection_id,kind,lease_token,last_attempt_at,in_progress)VALUES($1,$2,$3,$4,clock_timestamp(),true)ON CONFLICT(user_id,connection_id,kind)DO UPDATE SET lease_token=EXCLUDED.lease_token,last_attempt_at=EXCLUDED.last_attempt_at,in_progress=true`, l.Scope.UserID, l.Scope.ConnectionID, kind, l.Token)
		return storageError(e)
	})
}
func (r *platformRepo) FinishSync(ctx context.Context, l model.PlatformLease, kind, code string, duration int64, count int, more bool) error {
	if !validKind(kind) {
		return model.ErrInvalidArgument
	}
	switch code {
	case "", "auth_required", "unsupported", "parse_failed", "rate_limited", "verification_required", "invalid_argument", "upstream_unavailable":
	default:
		code = "internal"
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := platformLock(ctx, tx, l); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE platform_sync_status SET in_progress=false,last_finished_at=clock_timestamp(),last_success_at=CASE WHEN $5='' THEN clock_timestamp() ELSE last_success_at END,last_error_code=$5,last_duration_ms=$6,last_page_count=CASE WHEN $5='' THEN $7::integer ELSE NULL END,has_more=CASE WHEN $5='' THEN $8::boolean ELSE NULL END WHERE user_id=$1 AND connection_id=$2 AND kind=$3 AND lease_token=$4`, l.Scope.UserID, l.Scope.ConnectionID, kind, l.Token, code, duration, count, more)
		return storageError(e)
	})
}
func (r *previewRepo) Overview(ctx context.Context, owner string) ([]model.ConnectionPreview, error) {
	if owner == "" {
		return nil, model.ErrInvalidArgument
	}
	rows, e := r.db.pool.Query(ctx, `SELECT source,id,state,enabled,error_code,updated_at FROM platform_connections WHERE user_id=$1 UNION ALL SELECT 'dws',id,state,enabled,'',updated_at FROM dws_connections WHERE user_id=$1 ORDER BY source,id`, owner)
	if e != nil {
		return nil, storageError(e)
	}
	out := []model.ConnectionPreview{}
	for rows.Next() {
		var c model.ConnectionPreview
		if e = rows.Scan(&c.Source, &c.ConnectionID, &c.State, &c.Enabled, &c.ErrorCode, &c.UpdatedAt); e != nil {
			rows.Close()
			return nil, storageError(e)
		}
		c.Resources = []model.ResourcePreview{}
		out = append(out, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, storageError(e)
	}
	for i := range out {
		c := &out[i]
		var err error
		if c.Source == "dws" {
			c.Resources, err = r.dwsStats(ctx, owner, c.ConnectionID)
		} else {
			c.Resources, err = r.platformStats(ctx, owner, c.ConnectionID)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (r *previewRepo) platformStats(ctx context.Context, owner, connection string) ([]model.ResourcePreview, error) {
	rows, e := r.db.pool.Query(ctx, `WITH kinds AS(SELECT kind FROM platform_records WHERE user_id=$1 AND connection_id=$2 UNION SELECT split_part(kind,':',1) FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2 UNION SELECT kind FROM platform_sync_status WHERE user_id=$1 AND connection_id=$2), records AS(SELECT kind,count(*) AS versions,count(DISTINCT(external_id,parent_id))AS entities,max(observed_at)AS observed FROM platform_records WHERE user_id=$1 AND connection_id=$2 GROUP BY kind) SELECT k.kind,COALESCE(r.versions,0),COALESCE(r.entities,0),r.observed,COALESCE(s.last_success_at,p.updated_at),s.last_attempt_at,COALESCE(s.last_error_code,''),s.last_duration_ms,s.last_page_count,COALESCE(s.in_progress AND c.enabled AND c.lease_token=s.lease_token AND c.lease_until>clock_timestamp(),false),s.has_more FROM kinds k LEFT JOIN records r ON r.kind=k.kind LEFT JOIN platform_sync_status s ON s.user_id=$1 AND s.connection_id=$2 AND s.kind=k.kind LEFT JOIN (SELECT split_part(kind,':',1)AS kind,max(updated_at)AS updated_at FROM platform_checkpoints WHERE user_id=$1 AND connection_id=$2 GROUP BY split_part(kind,':',1)) p ON p.kind=k.kind JOIN platform_connections c ON c.user_id=$1 AND c.id=$2 ORDER BY k.kind`, owner, connection)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.ResourcePreview{}
	for rows.Next() {
		var s model.ResourcePreview
		if e = rows.Scan(&s.Kind, &s.RecordCount, &s.EntityCount, &s.LastObservedAt, &s.LastSuccessAt, &s.LastAttemptAt, &s.LastErrorCode, &s.LastDurationMS, &s.LastPageCount, &s.InProgress, &s.HasMore); e != nil {
			return nil, storageError(e)
		}
		out = append(out, s)
	}
	return out, storageError(rows.Err())
}
func (r *previewRepo) dwsStats(ctx context.Context, owner, connection string) ([]model.ResourcePreview, error) {
	s := model.ResourcePreview{Kind: "message"}
	e := r.db.pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT(group_id,external_id)),max(observed_at) FROM dws_messages WHERE user_id=$1 AND connection_id=$2`, owner, connection).Scan(&s.RecordCount, &s.EntityCount, &s.LastObservedAt)
	if e != nil {
		return nil, storageError(e)
	}
	e = r.db.pool.QueryRow(ctx, `SELECT max(created_at),max(updated_at)FILTER(WHERE state='succeeded'),EXISTS(SELECT 1 FROM dws_leases l JOIN dws_connections c ON c.user_id=l.user_id AND c.id=l.connection_id WHERE l.user_id=$1 AND l.connection_id=$2 AND c.enabled AND l.expires_at>clock_timestamp()) FROM dws_jobs WHERE user_id=$1 AND connection_id=$2`, owner, connection).Scan(&s.LastAttemptAt, &s.LastSuccessAt, &s.InProgress)
	if e != nil {
		return nil, storageError(e)
	}
	// A job error is shown only if the latest job is failed; old failures do not shadow later success.
	e = r.db.pool.QueryRow(ctx, `SELECT COALESCE((SELECT CASE WHEN state='failed' THEN error_code ELSE '' END FROM dws_jobs WHERE user_id=$1 AND connection_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1),'')`, owner, connection).Scan(&s.LastErrorCode)
	if e != nil {
		return nil, storageError(e)
	}
	return []model.ResourcePreview{s}, nil
}
func (r *previewRepo) Records(ctx context.Context, owner, source, connection, kind string, after int64, limit int) ([]model.PlatformRecord, error) {
	if owner == "" || connection == "" || after < 0 || limit < 1 || limit > 101 {
		return nil, model.ErrInvalidArgument
	}
	scope := model.DWSScope{UserID: owner, ConnectionID: connection}
	if source != "dws" {
		if !validSource(source) || (kind != "" && !validKind(kind)) {
			return nil, model.ErrInvalidArgument
		}
		p := &platformRepo{r.db}
		c, e := p.Get(ctx, scope)
		if e != nil {
			return nil, e
		}
		if c.Source != source {
			return nil, model.ErrNotFound
		}
		return p.Records(ctx, scope, kind, after, limit)
	}
	if kind != "" && kind != "message" {
		return nil, model.ErrInvalidArgument
	}
	if _, e := r.db.DWS().GetConnection(ctx, scope); e != nil {
		return nil, e
	}
	rows, e := r.db.pool.Query(ctx, `SELECT id,external_id,group_id,content_hash,content,observed_at FROM dws_messages WHERE user_id=$1 AND connection_id=$2 AND id>$3 ORDER BY id LIMIT $4`, owner, connection, after, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.PlatformRecord{}
	for rows.Next() {
		s := model.PlatformRecord{Source: "dws", Kind: "message"}
		var raw []byte
		if e = rows.Scan(&s.ID, &s.ExternalID, &s.ParentID, &s.Hash, &raw, &s.ObservedAt); e != nil {
			return nil, storageError(e)
		}
		if !json.Valid(raw) {
			return nil, model.ErrInternal
		}
		s.Content = raw
		out = append(out, s)
	}
	return out, storageError(rows.Err())
}

var _ model.PreviewRepository = (*previewRepo)(nil)
var _ model.SyncObserver = (*platformRepo)(nil)
