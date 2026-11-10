package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"github.com/jackc/pgx/v5"
)

// Migrate is explicit: Open never changes schema. Every migration and its ledger
// entry commit together under a PostgreSQL transaction advisory lock.
func (d *DB) Migrate(ctx context.Context) error {
	return d.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(730022400)`); e != nil {
			return storageError(e)
		}
		if _, e := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS dbbridge_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); e != nil {
			return storageError(e)
		}
		var version int
		if e := tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM dbbridge_migrations`).Scan(&version); e != nil {
			return storageError(e)
		}
		if version > 15 {
			return model.ErrConflict
		}
		for _, migration := range []struct {
			version int
			sql     string
		}{{1, schemaV1}, {2, schemaV2}, {3, schemaV3}, {4, schemaV4}, {5, schemaV5}, {6, schemaV6}, {7, schemaV7}, {8, schemaV8}, {9, schemaV9}, {10, schemaV10}, {11, schemaV11}, {12, schemaV12}, {13, schemaV13}, {14, schemaV14}, {15, schemaV15}} {
			if migration.version <= version {
				continue
			}
			if _, e := tx.Exec(ctx, migration.sql); e != nil {
				return storageError(e)
			}
			if _, e := tx.Exec(ctx, `INSERT INTO dbbridge_migrations(version) VALUES($1)`, migration.version); e != nil {
				return storageError(e)
			}
		}
		return nil
	})
}

const schemaV1 = `
CREATE TABLE users (
 id text PRIMARY KEY, username text NOT NULL UNIQUE, display_name text NOT NULL,
 password_hash text NOT NULL, role text NOT NULL CHECK(role IN ('admin','user')),
 disabled boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions(hash bytea PRIMARY KEY CHECK(octet_length(hash)=32), user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX sessions_user ON sessions(user_id);
CREATE TABLE dws_connections (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, id text NOT NULL,
 profile text NOT NULL, corp_id text NOT NULL DEFAULT '', upstream_user_id text NOT NULL DEFAULT '',
 mode text NOT NULL CHECK(mode='personal'), enabled boolean NOT NULL DEFAULT true,
 state text NOT NULL DEFAULT 'pending', workspace bytea NOT NULL DEFAULT '' CHECK(octet_length(workspace)<=22020096),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(user_id,id)
);
CREATE TABLE dws_groups(user_id text NOT NULL, connection_id text NOT NULL, id text NOT NULL, name text NOT NULL, PRIMARY KEY(user_id,connection_id,id), FOREIGN KEY(user_id,connection_id) REFERENCES dws_connections(user_id,id) ON DELETE CASCADE);
CREATE TABLE dws_leases(user_id text NOT NULL, connection_id text NOT NULL, group_id text NOT NULL, token bigint NOT NULL, expires_at timestamptz NOT NULL, PRIMARY KEY(user_id,connection_id), FOREIGN KEY(user_id,connection_id) REFERENCES dws_connections(user_id,id) ON DELETE CASCADE);
CREATE SEQUENCE dws_fencing_seq;
CREATE TABLE dws_checkpoints(user_id text NOT NULL, connection_id text NOT NULL, group_id text NOT NULL, cursor bytea NOT NULL DEFAULT '', cli_token bytea NOT NULL DEFAULT '', poll_watermark timestamptz NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(user_id,connection_id,group_id), FOREIGN KEY(user_id,connection_id,group_id) REFERENCES dws_groups(user_id,connection_id,id) ON DELETE CASCADE);
CREATE TABLE dws_messages(id bigserial PRIMARY KEY,user_id text NOT NULL,connection_id text NOT NULL,group_id text NOT NULL,external_id text NOT NULL,source_version text NOT NULL,content_hash text NOT NULL,content bytea NOT NULL CHECK(octet_length(content)<=1048576), observed_at timestamptz NOT NULL, UNIQUE(user_id,connection_id,group_id,external_id,source_version,content_hash), UNIQUE(user_id,connection_id,id), FOREIGN KEY(user_id,connection_id) REFERENCES dws_connections(user_id,id) ON DELETE CASCADE);
CREATE INDEX dws_messages_page ON dws_messages(user_id,connection_id,group_id,id);
CREATE TABLE dws_attachments(user_id text NOT NULL,connection_id text NOT NULL,message_id bigint NOT NULL,id text NOT NULL,name text NOT NULL,media_type text NOT NULL,sha256 text NOT NULL,data bytea NOT NULL CHECK(octet_length(data)<=8388608),PRIMARY KEY(user_id,connection_id,message_id,id), FOREIGN KEY(user_id,connection_id,message_id) REFERENCES dws_messages(user_id,connection_id,id) ON DELETE CASCADE);
CREATE TABLE dws_jobs(user_id text NOT NULL,connection_id text NOT NULL,id text NOT NULL,group_id text NOT NULL,state text NOT NULL CHECK(state IN ('queued','running','succeeded','failed','cancelled')),error_code text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,connection_id,id),FOREIGN KEY(user_id,connection_id) REFERENCES dws_connections(user_id,id) ON DELETE CASCADE);
`
