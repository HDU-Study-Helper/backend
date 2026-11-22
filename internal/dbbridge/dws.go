package dbbridge

import (
	"academic/backend/internal/model"
	"academic/backend/internal/trigger"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"time"
)

type dws struct{ db *DB }

var _ model.DWSRepository = (*dws)(nil)

const connCols = `id,user_id,profile,corp_id,upstream_user_id,mode,enabled,state,created_at,updated_at`

func scanConn(r pgx.Row) (c model.DWSConnection, e error) {
	e = r.Scan(&c.ID, &c.UserID, &c.Profile, &c.CorpID, &c.UpstreamUserID, &c.Mode, &c.Enabled, &c.State, &c.CreatedAt, &c.UpdatedAt)
	return c, storageError(e)
}
func validScope(s model.DWSScope) bool { return s.UserID != "" && s.ConnectionID != "" }
func (r *dws) CreateConnection(ctx context.Context, c model.DWSConnection) (model.DWSConnection, error) {
	if c.Mode == model.DWSModeSchool {
		return c, model.ErrUnsupported
	}
	if c.Mode == "" {
		c.Mode = model.DWSModePersonal
	}
	emptyIdentity := c.Profile == "" && c.CorpID == "" && c.UpstreamUserID == ""
	if c.UserID == "" || (!emptyIdentity && c.Profile == "") || (emptyIdentity && c.State != "pending") || len(c.Profile) > 512 || c.Mode != model.DWSModePersonal || len(c.WorkspaceCiphertext) != 0 {
		return c, model.ErrInvalidArgument
	}
	c.ID = newID()
	return scanConn(r.db.pool.QueryRow(ctx, `INSERT INTO dws_connections(user_id,id,profile,corp_id,upstream_user_id,mode,enabled,state) VALUES($1,$2,$3,$4,$5,$6,true,'pending') RETURNING `+connCols, c.UserID, c.ID, c.Profile, c.CorpID, c.UpstreamUserID, c.Mode))
}

// CreatePendingConnection serializes by owner so retries and concurrent servers reuse one empty row.
func (r *dws) CreatePendingConnection(ctx context.Context, owner string) (c model.DWSConnection, err error) {
	if owner == "" {
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
		c, e = scanConn(tx.QueryRow(ctx, `SELECT `+connCols+` FROM dws_connections WHERE user_id=$1 AND enabled AND state='pending' AND profile='' AND corp_id='' AND upstream_user_id='' AND octet_length(workspace)=0 ORDER BY created_at,id LIMIT 1 FOR UPDATE`, owner))
		if e == nil {
			return nil
		}
		if e != model.ErrNotFound {
			return e
		}
		c, e = scanConn(tx.QueryRow(ctx, `INSERT INTO dws_connections(user_id,id,profile,mode) VALUES($1,$2,'','personal') RETURNING `+connCols, owner, newID()))
		return e
	})
	return
}

func (r *dws) CompleteLink(ctx context.Context, l model.Lease, identity model.DWSIdentity, workspace []byte) error {
	if !validScope(l.Scope) || l.GroupID != "" || identity.CorpID == "" || identity.UpstreamUserID == "" || strings.ContainsAny(identity.CorpID+identity.UpstreamUserID, ":, \t\r\n\x00") || identity.Profile != identity.CorpID+":"+identity.UpstreamUserID || len(identity.Profile) > 512 || len(workspace) == 0 || len(workspace) > model.MaxWorkspaceBytes {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE dws_connections SET profile=$3,corp_id=$4,upstream_user_id=$5,workspace=$6,state='active',updated_at=now() WHERE user_id=$1 AND id=$2 AND mode='personal' AND state IN ('pending','active','expired','error') AND ((state='pending' AND profile='' AND corp_id='' AND upstream_user_id='' AND octet_length(workspace)=0) OR (profile=$3 AND corp_id=$4 AND upstream_user_id=$5))`, l.Scope.UserID, l.Scope.ConnectionID, identity.Profile, identity.CorpID, identity.UpstreamUserID, workspace)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() != 1 {
			return model.ErrConflict
		}
		return checkLease(ctx, tx, l)
	})
}

func (r *dws) GetConnection(ctx context.Context, s model.DWSScope) (model.DWSConnection, error) {
	return scanConn(r.db.pool.QueryRow(ctx, `SELECT `+connCols+` FROM dws_connections WHERE user_id=$1 AND id=$2`, s.UserID, s.ConnectionID))
}
func (r *dws) ListConnections(ctx context.Context, u string) ([]model.DWSConnection, error) {
	out := []model.DWSConnection{}
	rows, e := r.db.pool.Query(ctx, `SELECT `+connCols+` FROM dws_connections WHERE user_id=$1 AND enabled ORDER BY created_at,id LIMIT 100`, u)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanConn(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, storageError(rows.Err())
}
func (r *dws) UpdateConnection(ctx context.Context, s model.DWSScope, u model.DWSConnectionUpdate) (model.DWSConnection, error) {
	if u.Profile != nil && (*u.Profile == "" || len(*u.Profile) > 256) {
		return model.DWSConnection{}, model.ErrInvalidArgument
	}
	if u.State != nil && !validState(*u.State) {
		return model.DWSConnection{}, model.ErrInvalidArgument
	}
	return scanConn(r.db.pool.QueryRow(ctx, `UPDATE dws_connections SET profile=COALESCE($3,profile),corp_id=COALESCE($4,corp_id),upstream_user_id=COALESCE($5,upstream_user_id),state=COALESCE($6,state),updated_at=now() WHERE user_id=$1 AND id=$2 AND enabled AND (COALESCE($6,state)<>'active' OR (profile<>'' AND corp_id<>'' AND upstream_user_id<>'')) AND ($3::text IS NULL OR $3=profile) AND ($4::text IS NULL OR $4=corp_id) AND ($5::text IS NULL OR $5=upstream_user_id) RETURNING `+connCols, s.UserID, s.ConnectionID, u.Profile, u.CorpID, u.UpstreamUserID, u.State))
}
func validState(s string) bool {
	switch s {
	case "pending", "active", "expired", "error":
		return true
	}
	return false
}
func (r *dws) Disconnect(ctx context.Context, s model.DWSScope) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE dws_connections SET enabled=false,state='disabled',workspace='',updated_at=now() WHERE user_id=$1 AND id=$2`, s.UserID, s.ConnectionID)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() == 0 {
			return model.ErrNotFound
		}
		if _, e = tx.Exec(ctx, `UPDATE dws_leases SET token=nextval('dws_fencing_seq'),expires_at=now() WHERE user_id=$1 AND connection_id=$2`, s.UserID, s.ConnectionID); e != nil {
			return storageError(e)
		}
		if _, e = tx.Exec(ctx, `UPDATE dws_checkpoints SET cli_token='',cursor='' WHERE user_id=$1 AND connection_id=$2`, s.UserID, s.ConnectionID); e != nil {
			return storageError(e)
		}
		_, e = tx.Exec(ctx, `UPDATE dws_jobs SET state='cancelled',updated_at=now() WHERE user_id=$1 AND connection_id=$2 AND state IN ('queued','running')`, s.UserID, s.ConnectionID)
		return storageError(e)
	})
}
func (r *dws) DeleteConnection(ctx context.Context, s model.DWSScope) error {
	tag, e := r.db.pool.Exec(ctx, `DELETE FROM dws_connections WHERE user_id=$1 AND id=$2 AND NOT enabled`, s.UserID, s.ConnectionID)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}
func (r *dws) LoadWorkspace(ctx context.Context, s model.DWSScope) ([]byte, error) {
	var b []byte
	e := r.db.pool.QueryRow(ctx, `SELECT workspace FROM dws_connections WHERE user_id=$1 AND id=$2 AND enabled`, s.UserID, s.ConnectionID).Scan(&b)
	return b, storageError(e)
}

// Connection row is always locked before lease/group rows. Disconnect uses the
// same lock order, so a successful disconnect fences every later write.
func lockConn(ctx context.Context, tx pgx.Tx, s model.DWSScope) error {
	var enabled bool
	e := tx.QueryRow(ctx, `SELECT c.enabled AND NOT u.disabled FROM dws_connections c JOIN users u ON u.id=c.user_id WHERE c.user_id=$1 AND c.id=$2 FOR UPDATE OF u,c`, s.UserID, s.ConnectionID).Scan(&enabled)
	if e != nil {
		return storageError(e)
	}
	if !enabled {
		return model.ErrLeaseLost
	}
	return nil
}
func checkLease(ctx context.Context, tx pgx.Tx, l model.Lease) error {
	if e := lockConn(ctx, tx, l.Scope); e != nil {
		return e
	}
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dws_leases l WHERE l.user_id=$1 AND l.connection_id=$2 AND l.group_id=$3 AND l.token=$4 AND l.expires_at>clock_timestamp() AND (l.group_id='' OR EXISTS(SELECT 1 FROM dws_groups g WHERE (g.user_id,g.connection_id,g.id)=(l.user_id,l.connection_id,l.group_id))))`, l.Scope.UserID, l.Scope.ConnectionID, l.GroupID, l.Token).Scan(&ok)
	if e != nil {
		return storageError(e)
	}
	if !ok {
		return model.ErrLeaseLost
	}
	return nil
}
func (r *dws) SaveWorkspace(ctx context.Context, l model.Lease, b []byte) error {
	if len(b) > model.MaxWorkspaceBytes {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE dws_connections SET workspace=$3,updated_at=now() WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID, nonNil(b))
		return storageError(e)
	})
}
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
func (r *dws) SetGroups(ctx context.Context, s model.DWSScope, gs []model.Group) error {
	if len(gs) > 500 {
		return model.ErrInvalidArgument
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, g := range gs {
		if g.ID == "" || len(g.ID) > 512 || len(g.Name) > 1024 || seen[g.ID] {
			return model.ErrInvalidArgument
		}
		seen[g.ID] = true
		ids = append(ids, g.ID)
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := lockConn(ctx, tx, s); e != nil {
			return e
		}
		// Every explicit allowlist replacement fences in-flight work, including
		// connection-wide authentication/sync leases, before permissions change.
		if _, e := tx.Exec(ctx, `UPDATE dws_leases SET token=nextval('dws_fencing_seq'),expires_at=clock_timestamp() WHERE user_id=$1 AND connection_id=$2`, s.UserID, s.ConnectionID); e != nil {
			return storageError(e)
		}
		if _, e := tx.Exec(ctx, `DELETE FROM dws_groups WHERE user_id=$1 AND connection_id=$2 AND NOT(id=ANY($3::text[]))`, s.UserID, s.ConnectionID, ids); e != nil {
			return storageError(e)
		}
		for _, g := range gs {
			if _, e := tx.Exec(ctx, `INSERT INTO dws_groups(user_id,connection_id,id,name) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,connection_id,id) DO UPDATE SET name=excluded.name`, s.UserID, s.ConnectionID, g.ID, g.Name); e != nil {
				return storageError(e)
			}
		}
		return nil
	})
}
func (r *dws) ListGroups(ctx context.Context, s model.DWSScope) ([]model.Group, error) {
	if _, e := r.GetConnection(ctx, s); e != nil {
		return nil, e
	}
	out := []model.Group{}
	rows, e := r.db.pool.Query(ctx, `SELECT id,name FROM dws_groups WHERE user_id=$1 AND connection_id=$2 ORDER BY id`, s.UserID, s.ConnectionID)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var g model.Group
		if e := rows.Scan(&g.ID, &g.Name); e != nil {
			return nil, storageError(e)
		}
		out = append(out, g)
	}
	return out, storageError(rows.Err())
}
func validTTL(t time.Duration) bool { return t >= time.Second && t <= 10*time.Minute }
func (r *dws) AcquireLease(ctx context.Context, s model.DWSScope, g string, ttl time.Duration) (l model.Lease, e error) {
	l.Scope = s
	l.GroupID = g
	if !validScope(s) || !validTTL(ttl) {
		return l, model.ErrInvalidArgument
	}
	e = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := lockConn(ctx, tx, s); e != nil {
			return e
		}
		var allowed bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dws_groups WHERE user_id=$1 AND connection_id=$2 AND id=$3)`, s.UserID, s.ConnectionID, g).Scan(&allowed); e != nil {
			return storageError(e)
		}
		if !allowed && g != "" {
			return model.ErrNotFound
		}
		e := tx.QueryRow(ctx, `INSERT INTO dws_leases(user_id,connection_id,group_id,token,expires_at) VALUES($1,$2,$3,nextval('dws_fencing_seq'),clock_timestamp()+$4*interval '1 second') ON CONFLICT(user_id,connection_id) DO UPDATE SET group_id=excluded.group_id,token=nextval('dws_fencing_seq'),expires_at=excluded.expires_at WHERE dws_leases.expires_at<=clock_timestamp() RETURNING token,expires_at`, s.UserID, s.ConnectionID, g, ttl.Seconds()).Scan(&l.Token, &l.ExpiresAt)
		if e == pgx.ErrNoRows {
			return model.ErrConflict
		}
		return storageError(e)
	})
	return
}
func (r *dws) RenewLease(ctx context.Context, l model.Lease, ttl time.Duration) (model.Lease, error) {
	if !validTTL(ttl) {
		return l, model.ErrInvalidArgument
	}
	e := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		return storageError(tx.QueryRow(ctx, `UPDATE dws_leases SET expires_at=clock_timestamp()+$5*interval '1 second' WHERE user_id=$1 AND connection_id=$2 AND group_id=$3 AND token=$4 RETURNING expires_at`, l.Scope.UserID, l.Scope.ConnectionID, l.GroupID, l.Token, ttl.Seconds()).Scan(&l.ExpiresAt))
	})
	return l, e
}
func (r *dws) ReleaseLease(ctx context.Context, l model.Lease) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := lockConn(ctx, tx, l.Scope); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE dws_leases SET expires_at=clock_timestamp() WHERE user_id=$1 AND connection_id=$2 AND group_id=$3 AND token=$4`, l.Scope.UserID, l.Scope.ConnectionID, l.GroupID, l.Token)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() == 0 {
			return model.ErrLeaseLost
		}
		return nil
	})
}
func (r *dws) GetCheckpoint(ctx context.Context, s model.DWSScope, g string) (c model.Checkpoint, e error) {
	e = r.db.pool.QueryRow(ctx, `SELECT group_id,cursor,cli_token,poll_watermark,updated_at FROM dws_checkpoints WHERE user_id=$1 AND connection_id=$2 AND group_id=$3`, s.UserID, s.ConnectionID, g).Scan(&c.GroupID, &c.CursorCiphertext, &c.CLITokenCiphertext, &c.PollWatermark, &c.UpdatedAt)
	return c, storageError(e)
}
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func (r *dws) CommitSync(ctx context.Context, l model.Lease, c model.SyncCommit) error {
	totalBytes := 0
	for _, m := range c.Messages {
		totalBytes += len(m.Content)
		if totalBytes > 16<<20 {
			return model.ErrInvalidArgument
		}
	}
	if len(c.Messages) > 10000 || c.Checkpoint.GroupID == "" || (l.GroupID != "" && c.Checkpoint.GroupID != l.GroupID) || c.Checkpoint.PollWatermark.IsZero() || len(c.Checkpoint.CursorCiphertext) > model.MaxWorkspaceBytes || len(c.Checkpoint.CLITokenCiphertext) > model.MaxWorkspaceBytes || len(c.WorkspaceCiphertext) > model.MaxWorkspaceBytes {
		return model.ErrInvalidArgument
	}
	created, updated, unchanged := 0, 0, 0
	var snapshots []model.ControlChange
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		groupID := c.Checkpoint.GroupID
		var allowed bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dws_groups WHERE user_id=$1 AND connection_id=$2 AND id=$3)`, l.Scope.UserID, l.Scope.ConnectionID, groupID).Scan(&allowed); e != nil {
			return storageError(e)
		}
		if !allowed {
			return model.ErrNotFound
		}
		for _, m := range c.Messages {
			if m.GroupID != groupID || m.ExternalID == "" || len(m.ExternalID) > 512 || len(m.SourceVersion) > 512 || !validHash(m.ContentHash) || len(m.Content) > model.MaxMessageBytes || m.ObservedAt.IsZero() {
				return model.ErrInvalidArgument
			}
			var version, hash string
			var before []byte
			lookupErr := tx.QueryRow(ctx, `SELECT source_version,content_hash,content FROM dws_messages WHERE user_id=$1 AND connection_id=$2 AND group_id=$3 AND external_id=$4`, l.Scope.UserID, l.Scope.ConnectionID, groupID, m.ExternalID).Scan(&version, &hash, &before)
			if lookupErr != nil && lookupErr != pgx.ErrNoRows {
				return storageError(lookupErr)
			}
			operation := "updated"
			if lookupErr == pgx.ErrNoRows {
				created++
				operation = "created"
			} else if version != m.SourceVersion || hash != m.ContentHash {
				updated++
			} else {
				unchanged++
				continue
			}
			snapshots = append(snapshots, model.ControlChange{Operation: operation, Resource: "message", ConnectionID: l.Scope.ConnectionID, GroupID: groupID, RecordID: m.ExternalID, Before: before, After: m.Content, OccurredAt: m.ObservedAt})

			if _, e := tx.Exec(ctx, `INSERT INTO dws_messages(user_id,connection_id,group_id,external_id,source_version,content_hash,content,observed_at,sent_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(user_id,connection_id,group_id,external_id) DO UPDATE SET source_version=EXCLUDED.source_version,content_hash=EXCLUDED.content_hash,content=EXCLUDED.content,observed_at=EXCLUDED.observed_at,sent_at=EXCLUDED.sent_at WHERE dws_messages.source_version<>EXCLUDED.source_version OR dws_messages.content_hash<>EXCLUDED.content_hash`, l.Scope.UserID, l.Scope.ConnectionID, groupID, m.ExternalID, m.SourceVersion, m.ContentHash, nonNil(m.Content), m.ObservedAt, sourceSentTime(m.Content)); e != nil {
				return storageError(e)
			}
		}
		if e := captureControl(ctx, tx, l.Scope.UserID, "dws", snapshots); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO dws_checkpoints(user_id,connection_id,group_id,cursor,cli_token,poll_watermark) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,connection_id,group_id) DO UPDATE SET cursor=excluded.cursor,cli_token=excluded.cli_token,poll_watermark=excluded.poll_watermark,updated_at=now() WHERE dws_checkpoints.poll_watermark<=excluded.poll_watermark`, l.Scope.UserID, l.Scope.ConnectionID, groupID, nonNil(c.Checkpoint.CursorCiphertext), nonNil(c.Checkpoint.CLITokenCiphertext), c.Checkpoint.PollWatermark)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() == 0 {
			return model.ErrConflict
		}
		if c.WorkspaceCiphertext != nil {
			if _, e := tx.Exec(ctx, `UPDATE dws_connections SET workspace=$3,updated_at=now() WHERE user_id=$1 AND id=$2`, l.Scope.UserID, l.Scope.ConnectionID, c.WorkspaceCiphertext); e != nil {
				return storageError(e)
			}
		}
		if c.JobID != "" {
			tag, e := tx.Exec(ctx, `UPDATE dws_jobs SET state='succeeded',updated_at=now() WHERE user_id=$1 AND connection_id=$2 AND id=$3 AND group_id=$4 AND state IN ('queued','running')`, l.Scope.UserID, l.Scope.ConnectionID, c.JobID, groupID)
			if e != nil {
				return storageError(e)
			}
			if tag.RowsAffected() == 0 {
				return model.ErrConflict
			}
		}
		// A lease may expire during a large transaction; check again before commit.
		return checkLease(ctx, tx, l)
	})
	if err == nil {
		model.RefreshCommitted(ctx, "dws", created, updated, unchanged)
	}
	return err
}

func (r *dws) ListMessages(ctx context.Context, s model.DWSScope, g string, after int64, limit int) ([]model.Message, error) {
	if limit < 1 || limit > 100 || after < 0 {
		return nil, model.ErrInvalidArgument
	}
	if _, e := r.GetConnection(ctx, s); e != nil {
		return nil, e
	}
	out := []model.Message{}
	rows, e := r.db.pool.Query(ctx, `SELECT id,group_id,external_id,source_version,content_hash,content,observed_at FROM dws_messages WHERE user_id=$1 AND connection_id=$2 AND group_id=$3 AND id>$4 ORDER BY id LIMIT $5`, s.UserID, s.ConnectionID, g, after, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var m model.Message
		if e := rows.Scan(&m.ID, &m.GroupID, &m.ExternalID, &m.SourceVersion, &m.ContentHash, &m.Content, &m.ObservedAt); e != nil {
			return nil, storageError(e)
		}
		out = append(out, m)
	}
	return out, storageError(rows.Err())
}
func (r *dws) PutAttachment(ctx context.Context, l model.Lease, a model.Attachment) error {
	sum := sha256.Sum256(a.Data)
	if a.ID == "" || len(a.ID) > 512 || len(a.Name) > 1024 || len(a.MediaType) > 256 || len(a.Data) > model.MaxAttachmentBytes || a.SHA256 != hex.EncodeToString(sum[:]) {
		return model.ErrInvalidArgument
	}
	var changes []trigger.Change
	err := r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		var groupID string
		if e := tx.QueryRow(ctx, `SELECT group_id FROM dws_messages WHERE user_id=$1 AND connection_id=$2 AND ($3='' OR group_id=$3) AND id=$4 AND EXISTS(SELECT 1 FROM dws_groups g WHERE (g.user_id,g.connection_id,g.id)=(dws_messages.user_id,dws_messages.connection_id,dws_messages.group_id))`, l.Scope.UserID, l.Scope.ConnectionID, l.GroupID, a.MessageID).Scan(&groupID); e != nil {
			return storageError(e)
		}
		var oldName, oldMedia, oldHash string
		e := tx.QueryRow(ctx, `SELECT name,media_type,sha256 FROM dws_attachments WHERE user_id=$1 AND connection_id=$2 AND message_id=$3 AND id=$4`, l.Scope.UserID, l.Scope.ConnectionID, a.MessageID, a.ID).Scan(&oldName, &oldMedia, &oldHash)
		if e != nil && e != pgx.ErrNoRows {
			return storageError(e)
		}
		operation := trigger.Updated
		var before []byte
		if e == pgx.ErrNoRows {
			operation = trigger.Created
		} else {
			before, _ = json.Marshal(map[string]string{"name": oldName, "media_type": oldMedia, "sha256": oldHash})
		}
		tag, e := tx.Exec(ctx, `INSERT INTO dws_attachments(user_id,connection_id,message_id,id,name,media_type,sha256,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(user_id,connection_id,message_id,id) DO UPDATE SET name=EXCLUDED.name,media_type=EXCLUDED.media_type,sha256=EXCLUDED.sha256,data=EXCLUDED.data WHERE dws_attachments.sha256<>EXCLUDED.sha256 OR dws_attachments.name<>EXCLUDED.name OR dws_attachments.media_type<>EXCLUDED.media_type`, l.Scope.UserID, l.Scope.ConnectionID, a.MessageID, a.ID, a.Name, a.MediaType, a.SHA256, nonNil(a.Data))
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() > 0 {
			after, _ := json.Marshal(map[string]string{"name": a.Name, "media_type": a.MediaType, "sha256": a.SHA256})
			changes = append(changes, trigger.Change{UserID: l.Scope.UserID, ConnectionID: l.Scope.ConnectionID, Source: "dws", Resource: "attachment", RecordID: strconv.FormatInt(a.MessageID, 10) + ":" + a.ID, ExternalID: a.ID, ParentID: strconv.FormatInt(a.MessageID, 10), Operation: operation, ChangedFields: trigger.FieldChanges(before, after), OccurredAt: time.Now().UTC()})
			if e := captureControl(ctx, tx, l.Scope.UserID, "dws", []model.ControlChange{{Operation: string(operation), Resource: "attachment", ConnectionID: l.Scope.ConnectionID, GroupID: groupID, RecordID: strconv.FormatInt(a.MessageID, 10) + ":" + a.ID, Before: before, After: after, ChangedFields: trigger.FieldChanges(before, after), OccurredAt: time.Now().UTC()}}); e != nil {
				return e
			}

		}
		return checkLease(ctx, tx, l)
	})
	if err == nil {
		r.db.emit(ctx, changes)
	}
	return err
}
func (r *dws) GetAttachment(ctx context.Context, s model.DWSScope, m int64, id string) (a model.Attachment, e error) {
	e = r.db.pool.QueryRow(ctx, `SELECT id,message_id,name,media_type,sha256,data FROM dws_attachments WHERE user_id=$1 AND connection_id=$2 AND message_id=$3 AND id=$4`, s.UserID, s.ConnectionID, m, id).Scan(&a.ID, &a.MessageID, &a.Name, &a.MediaType, &a.SHA256, &a.Data)
	return a, storageError(e)
}
func scanJob(row pgx.Row) (j model.SyncJob, e error) {
	e = row.Scan(&j.ID, &j.GroupID, &j.State, &j.ErrorCode, &j.CreatedAt, &j.UpdatedAt)
	return j, storageError(e)
}
func (r *dws) CreateJob(ctx context.Context, s model.DWSScope, g string) (j model.SyncJob, e error) {
	e = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := lockConn(ctx, tx, s); e != nil {
			return e
		}
		var ok bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dws_groups WHERE user_id=$1 AND connection_id=$2 AND id=$3)`, s.UserID, s.ConnectionID, g).Scan(&ok); e != nil {
			return storageError(e)
		}
		if !ok {
			return model.ErrNotFound
		}
		var e error
		j, e = scanJob(tx.QueryRow(ctx, `INSERT INTO dws_jobs(user_id,connection_id,id,group_id,state) VALUES($1,$2,$3,$4,'queued') RETURNING id,group_id,state,error_code,created_at,updated_at`, s.UserID, s.ConnectionID, newID(), g))
		return e
	})
	return
}
func (r *dws) GetJob(ctx context.Context, s model.DWSScope, id string) (model.SyncJob, error) {
	return scanJob(r.db.pool.QueryRow(ctx, `SELECT id,group_id,state,error_code,created_at,updated_at FROM dws_jobs WHERE user_id=$1 AND connection_id=$2 AND id=$3`, s.UserID, s.ConnectionID, id))
}
func (r *dws) FailJob(ctx context.Context, s model.DWSScope, id, code string) error {
	switch code {
	case "upstream_unavailable", "auth_expired", "auth_required", "conflict", "budget_exceeded", "parse_failed", "cancelled", "internal", "lease_lost", "unsupported", "invalid_argument", "not_found", "rate_limited":
	default:
		return model.ErrInvalidArgument
	}
	tag, e := r.db.pool.Exec(ctx, `UPDATE dws_jobs SET state='failed',error_code=$4,updated_at=now() WHERE user_id=$1 AND connection_id=$2 AND id=$3 AND state='queued'`, s.UserID, s.ConnectionID, id, code)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}
