package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/defaultroles"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Called by the application before serving; existing roles (even disabled or
// customized ones) are preserved. New application users are seeded in their
// account-creation transaction. Standalone repository consumers remain opt-in.
func (d *DB) EnableDefaultRoles(ctx context.Context) error {
	defs, e := defaultroles.Definitions()
	if e != nil {
		return e
	}
	e = d.transaction(ctx, func(tx pgx.Tx) error {
		if e := lockUsers(ctx, tx); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id FROM users ORDER BY id`)
		if e != nil {
			return e
		}
		owners, e := pgx.CollectRows(rows, pgx.RowTo[string])
		if e != nil {
			return e
		}
		for _, owner := range owners {
			if e := seedRoles(ctx, tx, owner, defs); e != nil {
				return e
			}
		}
		return nil
	})
	if e == nil {
		d.defaultRoles = defs
	}
	return e
}
func seedRoles(ctx context.Context, tx pgx.Tx, owner string, defs []f.AgentDefinition) error {
	if len(defs) == 0 {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT config->>'agent_type',agent_id FROM agent_instances WHERE owner_user_id=$1 AND config->>'agent_type' IN ('trigger','learner','worker','filter','chat') ORDER BY (status='enabled') DESC, created_at, agent_id`, owner)
	if e != nil {
		return e
	}
	ids := map[string]string{}
	for rows.Next() {
		var role, id string
		if e = rows.Scan(&role, &id); e != nil {
			rows.Close()
			return e
		}
		if ids[role] == "" {
			ids[role] = id
		}
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, d := range defs {
		if ids[d.Type] != "" {
			continue
		}
		id := newID()
		ids[d.Type] = id
		d.SystemPrompt = strings.NewReplacer("{{LEARNER_AGENT_ID}}", ids["learner"], "{{WORKER_AGENT_ID}}", ids["worker"]).Replace(d.SystemPrompt)
		raw, e := json.Marshal(d)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO agent_instances(agent_id,owner_user_id,config,status) VALUES($1,$2,$3,'enabled')`, id, owner, raw); e != nil {
			return e
		}
	}
	return nil
}
func (r *users) insertWithRoles(ctx context.Context, tx pgx.Tx, u model.User) (model.User, error) {
	v, e := insertUser(ctx, tx, u)
	if e == nil {
		e = seedRoles(ctx, tx, v.ID, r.db.defaultRoles)
	}
	return v, e
}
