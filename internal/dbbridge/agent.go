package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	f "academic/agent/framework"
	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type agents struct{ db *DB }

func (d *DB) Agents() f.Repository { return &agents{db: d} }
func agentError(err error) error {
	if err == nil {
		return nil
	}
	var known *f.Error
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return f.Fail("RESOURCE_NOT_FOUND", "resource not found in current scope")
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "23505":
			return f.Fail("INVALID_ARGUMENT", "resource already exists")
		case "23503":
			return f.Fail("PERMISSION_DENIED", "object ownership or reference constraint failed")
		case "23514", "22001":
			return f.Fail("INVALID_ARGUMENT", "invalid record")
		}
	}
	return f.Fail("INTERNAL_ERROR", "agent storage unavailable")
}
func (r *agents) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return agentError(r.db.transaction(ctx, func(tx pgx.Tx) error { return fn(tx) }))
}
func activeAgent(ctx context.Context, tx pgx.Tx, s f.Scope) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT a.agent_id FROM agent_instances a JOIN users u ON u.id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND NOT u.disabled FOR SHARE OF a,u`, s.OwnerID, s.AgentID).Scan(&id)
	if err != nil {
		return agentError(err)
	}
	return nil
}
func activeOwner(ctx context.Context, tx pgx.Tx, owner string) error {
	var id string
	return agentError(tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled FOR SHARE`, owner).Scan(&id))
}
func (r *agents) PutAgent(ctx context.Context, a f.AgentInstance) (result f.AgentInstance, err error) {
	if a.ID == "" || a.OwnerID == "" || a.Name == "" || a.Model.Provider == "" || (a.Model.Model == "" && a.Model.Mode != "default") || (a.Model.Mode != "" && a.Model.Mode != "default" && a.Model.Mode != "custom") || (a.Status != "enabled" && a.Status != "disabled") {
		return result, f.Fail("INVALID_ARGUMENT", "invalid agent")
	}
	a.Limits = a.Limits.Defaults()
	raw, e := json.Marshal(a.AgentDefinition)
	if e != nil || len(raw) > 1<<20 {
		return result, f.Fail("INVALID_ARGUMENT", "invalid agent configuration")
	}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, a.OwnerID); e != nil {
			return e
		}
		return agentError(tx.QueryRow(ctx, `INSERT INTO agent_instances(agent_id,owner_user_id,config,status) VALUES($1,$2,$3,$4) ON CONFLICT(agent_id) DO UPDATE SET config=EXCLUDED.config,status=EXCLUDED.status,updated_at=clock_timestamp() WHERE agent_instances.owner_user_id=EXCLUDED.owner_user_id RETURNING created_at,updated_at`, a.ID, a.OwnerID, raw, a.Status).Scan(&a.CreatedAt, &a.UpdatedAt))
	})
	return a, err
}
func scanAgent(row pgx.Row) (a f.AgentInstance, err error) {
	var raw []byte
	err = row.Scan(&a.ID, &a.OwnerID, &raw, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return a, agentError(err)
	}
	if json.Unmarshal(raw, &a.AgentDefinition) != nil {
		return a, f.Fail("INTERNAL_ERROR", "invalid stored agent")
	}
	return a, nil
}

const agentCols = `a.agent_id,a.owner_user_id,a.config,a.status,a.created_at,a.updated_at`

func (r *agents) GetAgent(ctx context.Context, s f.Scope) (f.AgentInstance, error) {
	a, err := scanAgent(r.db.pool.QueryRow(ctx, `SELECT `+agentCols+` FROM agent_instances a JOIN users u ON u.id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND NOT u.disabled`, s.OwnerID, s.AgentID))
	if err != nil {
		var e *f.Error
		if errors.As(err, &e) && e.Code == "RESOURCE_NOT_FOUND" {
			return a, f.Fail("AGENT_NOT_FOUND", "agent not found in current scope")
		}
	}
	return a, err
}
func (r *agents) ListAgents(ctx context.Context, owner string, limit int, after string) ([]f.AgentInstance, error) {
	if limit < 1 || limit > 100 {
		return nil, f.Fail("INVALID_ARGUMENT", "invalid page size")
	}
	rows, err := r.db.pool.Query(ctx, `SELECT `+agentCols+` FROM agent_instances a JOIN users u ON u.id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.agent_id>$2 AND NOT u.disabled ORDER BY a.agent_id LIMIT $3`, owner, after, limit)
	if err != nil {
		return nil, agentError(err)
	}
	defer rows.Close()
	out := []f.AgentInstance{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, agentError(rows.Err())
}
func (r *agents) DeleteAgent(ctx context.Context, s f.Scope) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		if err := activeAgent(ctx, tx, s); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM agent_instances WHERE owner_user_id=$1 AND agent_id=$2`, s.OwnerID, s.AgentID)
		return affected(tag, err)
	})
}
func affected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return agentError(err)
	}
	if tag.RowsAffected() == 0 {
		return f.Fail("RESOURCE_NOT_FOUND", "resource not found in current scope")
	}
	return nil
}

const workflowCols = `workflow_id,owner_user_id,name,description,COALESCE(parent_workflow_id,''),definition,version,status,created_by_agent_id,created_at,updated_at`

func scanWorkflow(row pgx.Row) (w f.Workflow, err error) {
	err = row.Scan(&w.ID, &w.OwnerID, &w.Name, &w.Description, &w.ParentID, &w.Definition, &w.Version, &w.Status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt)
	return w, agentError(err)
}
func (r *agents) SaveWorkflow(ctx context.Context, s f.Scope, w f.Workflow, create bool) (result f.Workflow, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		var e error
		result, e = saveWorkflowTx(ctx, tx, s, w, create)
		return e
	})
	if err != nil {
		return f.Workflow{}, err
	}
	return result, nil
}

// saveWorkflowTx is shared with the atomic private profile initializer. Callers
// must use a single transaction and must not return its result before commit.
func saveWorkflowTx(ctx context.Context, tx pgx.Tx, s f.Scope, w f.Workflow, create bool) (result f.Workflow, err error) {
	var definition map[string]any
	if strings.TrimSpace(w.Name) == "" || len(w.Name) > 1024 || len(w.Definition) > 1<<20 || json.Unmarshal(w.Definition, &definition) != nil || definition == nil {
		return result, f.Fail("INVALID_ARGUMENT", "invalid workflow definition")
	}
	if w.Status == "" {
		w.Status = "draft"
	}
	if w.Status == "active" && !model.ValidWorkflowEntity(w.Definition) {
		return result, f.Fail("INVALID_ARGUMENT", "active Workflow必须具有具体entity：kind为course/competition/task/identity，id和name不可为课程、网课、考试等泛称")
	}
	if w.Status != "draft" && w.Status != "active" && w.Status != "archived" {
		return result, f.Fail("INVALID_ARGUMENT", "invalid workflow status")
	}
	err = func() error {
		// Serializes workflow tree writes per owner, preventing concurrent cycle creation.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,730022405))`, s.OwnerID); e != nil {
			return agentError(e)
		}
		if e := activeAgent(ctx, tx, s); e != nil {
			return e
		}
		if w.Status == "active" {
			entity := definition["entity"].(map[string]any)
			var existing string
			e := tx.QueryRow(ctx, `SELECT workflow_id FROM agent_workflows WHERE owner_user_id=$1 AND status='active' AND definition->'entity'->>'kind'=$2 AND definition->'entity'->>'id'=$3 AND workflow_id<>$4 AND (name=$5 OR (COALESCE(definition->>'goal','')<>'' AND definition->>'goal'=$6)) LIMIT 1`, s.OwnerID, entity["kind"], entity["id"], w.ID, w.Name, definition["goal"]).Scan(&existing)
			if e == nil {
				return f.Fail("CONFLICT", "该具体实体已有同名或同目标active Workflow："+existing+"；请workflow_get核查后复用或最小更新，不要重复创建")
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return agentError(e)
			}
		}
		if create {
			w.ID = f.NewID()
			w.Version = 1
			w.CreatedBy = s.AgentID
		} else {
			old, e := scanWorkflow(tx.QueryRow(ctx, `SELECT `+workflowCols+` FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2 FOR UPDATE`, s.OwnerID, w.ID))
			if e != nil {
				return e
			}
			if old.Version != w.Version {
				return f.Fail("INVALID_ARGUMENT", "workflow version conflict")
			}
			if old.ParentID != w.ParentID {
				return f.Fail("INVALID_ARGUMENT", "workflow parent is immutable")
			}
			w.Version++
			w.CreatedBy = old.CreatedBy
		}
		if w.ParentID != "" {
			if w.ParentID == w.ID {
				return f.Fail("INVALID_ARGUMENT", "workflow cannot parent itself")
			}
			var parent string
			if e := tx.QueryRow(ctx, `SELECT workflow_id FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, s.OwnerID, w.ParentID).Scan(&parent); e != nil {
				return f.Fail("PERMISSION_DENIED", "workflow parent is not owned by current user")
			}
		}
		var e error
		if create {
			result, e = scanWorkflow(tx.QueryRow(ctx, `INSERT INTO agent_workflows(workflow_id,owner_user_id,name,description,parent_workflow_id,definition,version,status,created_by_agent_id) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9) RETURNING `+workflowCols, w.ID, s.OwnerID, w.Name, w.Description, w.ParentID, w.Definition, w.Version, w.Status, w.CreatedBy))
		} else {
			result, e = scanWorkflow(tx.QueryRow(ctx, `UPDATE agent_workflows SET name=$3,description=$4,definition=$5,version=$6,status=$7,updated_at=clock_timestamp() WHERE owner_user_id=$1 AND workflow_id=$2 RETURNING `+workflowCols, s.OwnerID, w.ID, w.Name, w.Description, w.Definition, w.Version, w.Status))
		}
		if e != nil {
			return e
		}
		raw, e := json.Marshal(result)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO agent_workflow_versions(owner_user_id,workflow_id,version,snapshot) VALUES($1,$2,$3,$4)`, s.OwnerID, w.ID, w.Version, raw)
		return agentError(e)
	}()
	return
}
func (r *agents) GetWorkflow(ctx context.Context, owner, id string) (result f.Workflow, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		var e error
		result, e = scanWorkflow(tx.QueryRow(ctx, `SELECT `+workflowCols+` FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, owner, id))
		return e
	})
	return
}
func (r *agents) ListWorkflows(ctx context.Context, owner string, limit int, after string) (out []f.Workflow, err error) {
	if limit < 1 || limit > 100 {
		return nil, f.Fail("INVALID_ARGUMENT", "invalid workflow page size")
	}
	out = []f.Workflow{}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+workflowCols+` FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id>$2 ORDER BY workflow_id LIMIT $3`, owner, after, limit)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			w, e := scanWorkflow(rows)
			if e != nil {
				return e
			}
			out = append(out, w)
		}
		return agentError(rows.Err())
	})
	return
}
func (r *agents) WorkflowVersions(ctx context.Context, owner, id string) (out []f.Workflow, err error) {
	out = []f.Workflow{}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		var exists string
		if e := tx.QueryRow(ctx, `SELECT workflow_id FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, owner, id).Scan(&exists); e != nil {
			return agentError(e)
		}
		rows, e := tx.Query(ctx, `SELECT snapshot FROM agent_workflow_versions WHERE owner_user_id=$1 AND workflow_id=$2 ORDER BY version`, owner, id)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				return agentError(e)
			}
			var w f.Workflow
			if json.Unmarshal(raw, &w) != nil {
				return f.Fail("INTERNAL_ERROR", "invalid workflow snapshot")
			}
			out = append(out, w)
		}
		return agentError(rows.Err())
	})
	return
}
func (r *agents) DeleteWorkflow(ctx context.Context, owner, id string) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `DELETE FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, owner, id)
		return affected(tag, e)
	})
}
func (r *agents) SaveRun(ctx context.Context, record f.RunRecord) error {
	if record.ID == "" || record.StartedAt.IsZero() {
		return f.Fail("INVALID_ARGUMENT", "invalid run record")
	}
	raw, e := json.Marshal(record)
	if e != nil {
		return f.Fail("INVALID_ARGUMENT", "invalid run")
	}
	return r.tx(ctx, func(tx pgx.Tx) error {
		// Finalization may run after owner disablement; it records an already authorized run only.
		if record.Status == "running" {
			if e := activeAgent(ctx, tx, f.Scope{OwnerID: record.OwnerID, AgentID: record.AgentID}); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `INSERT INTO agent_runs(run_id,owner_user_id,agent_id,parent_run_id,record,status,started_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7)`, record.ID, record.OwnerID, record.AgentID, record.ParentRunID, raw, record.Status, record.StartedAt)
			return agentError(e)
		}
		tag, e := tx.Exec(ctx, `UPDATE agent_runs SET record=$4,status=$5,finished_at=$6 WHERE owner_user_id=$1 AND agent_id=$2 AND run_id=$3 AND status='running'`, record.OwnerID, record.AgentID, record.ID, raw, record.Status, record.FinishedAt)
		return affected(tag, e)
	})
}
func (r *agents) GetRun(ctx context.Context, owner, id string) (record f.RunRecord, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		var raw []byte
		if e := tx.QueryRow(ctx, `SELECT record FROM agent_runs WHERE owner_user_id=$1 AND run_id=$2`, owner, id).Scan(&raw); e != nil {
			return agentError(e)
		}
		if json.Unmarshal(raw, &record) != nil {
			return f.Fail("INTERNAL_ERROR", "invalid run record")
		}
		return nil
	})
	return
}

var _ f.Repository = (*agents)(nil)
