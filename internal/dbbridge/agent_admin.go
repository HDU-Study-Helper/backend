package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

const schemaV6 = `CREATE TABLE agent_framework_settings (
 id integer PRIMARY KEY CHECK(id=1), encrypted_settings bytea NOT NULL CHECK(octet_length(encrypted_settings)<=1048576),
 version integer NOT NULL CHECK(version>0), updated_at timestamptz NOT NULL DEFAULT now()
);`

type agentAdminRepo struct{ db *DB }

func (d *DB) AgentAdmin() model.AgentAdminRepository { return &agentAdminRepo{db: d} }
func (r *agentAdminRepo) LoadAgentSettings(ctx context.Context) (raw []byte, version int, err error) {
	err = storageError(r.db.pool.QueryRow(ctx, `SELECT encrypted_settings,version FROM agent_framework_settings WHERE id=1`).Scan(&raw, &version))
	return
}
func (r *agentAdminRepo) SaveAgentSettings(ctx context.Context, raw []byte, expected int) (version int, err error) {
	if len(raw) == 0 || len(raw) > 1<<20 || expected < 0 {
		return 0, model.ErrInvalidArgument
	}
	if expected == 0 {
		err = storageError(r.db.pool.QueryRow(ctx, `INSERT INTO agent_framework_settings(id,encrypted_settings,version) VALUES(1,$1,1) RETURNING version`, raw).Scan(&version))
	} else {
		err = storageError(r.db.pool.QueryRow(ctx, `UPDATE agent_framework_settings SET encrypted_settings=$1,version=version+1,updated_at=clock_timestamp() WHERE id=1 AND version=$2 RETURNING version`, raw, expected).Scan(&version))
		if err == model.ErrNotFound {
			err = model.ErrConflict
		}
	}
	return
}
func (r *agentAdminRepo) ListAgentRuns(ctx context.Context, owner, agentID string, limit int) (out []f.RunRecord, err error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidArgument
	}
	out = []f.RunRecord{}
	err = (&agents{db: r.db}).tx(ctx, func(tx pgx.Tx) error {
		if e := activeAgent(ctx, tx, f.Scope{OwnerID: owner, AgentID: agentID}); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT record FROM agent_runs WHERE owner_user_id=$1 AND agent_id=$2 ORDER BY started_at DESC,run_id DESC LIMIT $3`, owner, agentID, limit)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				return agentError(e)
			}
			var run f.RunRecord
			if json.Unmarshal(raw, &run) != nil {
				return f.Fail("INTERNAL_ERROR", "invalid run record")
			}
			out = append(out, run)
		}
		return agentError(rows.Err())
	})
	return
}
