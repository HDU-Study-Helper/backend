package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type actionRepo struct{ db *DB }

func (d *DB) Actions() model.ActionRepository { return &actionRepo{d} }

const actionCols = `id,kind,status,source_agent_id,source_run_id,input,result,result_ref,updated_at`

func scanAction(row pgx.Row) (a model.PublicAction, err error) {
	err = storageError(row.Scan(&a.ID, &a.Kind, &a.Status, &a.AgentID, &a.RunID, &a.Input, &a.Result, &a.ResultRef, &a.Updated))
	return
}

// Serializing short mutations on the owner makes receipts and todo target updates atomic.
func actionActor(ctx context.Context, tx pgx.Tx, s model.ActionScope) error {
	var n int
	return storageError(tx.QueryRow(ctx, `SELECT 1 FROM users u JOIN agent_instances a ON a.owner_user_id=u.id JOIN agent_runs r ON r.owner_user_id=u.id AND r.agent_id=a.agent_id WHERE u.id=$1 AND NOT u.disabled AND a.agent_id=$2 AND a.status='enabled' AND r.run_id=$3 FOR UPDATE OF u`, s.OwnerID, s.AgentID, s.RunID).Scan(&n))
}
func (r *actionRepo) PutAction(ctx context.Context, s model.ActionScope, kind string, in model.ActionInput) (a model.PublicAction, err error) {
	if err = in.Validate(kind); err != nil {
		return
	}
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > 49152 {
		return a, model.ErrInvalidArgument
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := actionActor(ctx, tx, s); e != nil {
			return e
		}
		var id string
		var same bool
		e := tx.QueryRow(ctx, `SELECT action_id,request=$4::jsonb FROM action_receipts WHERE owner_id=$1 AND kind=$2 AND request_key=$3`, s.OwnerID, kind, in.Key, raw).Scan(&id, &same)
		if e == nil {
			if !same {
				return model.ErrConflict
			}
			a, e = scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+` FROM public_actions WHERE owner_id=$1 AND id=$2`, s.OwnerID, id))
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return storageError(e)
		}
		var rev *time.Time
		if in.Mode != "" {
			rev, e = reminderProof(ctx, tx, s, in)
			if e != nil {
				return e
			}
			var prior model.PublicAction
			var found bool
			prior, found, e = previousReminder(ctx, tx, s, in, rev)
			if e != nil {
				return e
			}
			if found {
				a = prior
				_, e = tx.Exec(ctx, `INSERT INTO action_receipts(owner_id,kind,request_key,action_id,request) VALUES($1,$2,$3,$4,$5)`, s.OwnerID, kind, in.Key, a.ID, raw)
				return storageError(e)
			}
		}
		status := map[string]string{"todo": "open", "notice": "recorded", "job": "pending"}[kind]
		a, e = scanAction(tx.QueryRow(ctx, `INSERT INTO public_actions(owner_id,id,kind,target_key,source_agent_id,source_run_id,status,input,due_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9) ON CONFLICT(owner_id,kind,target_key) DO UPDATE SET source_agent_id=EXCLUDED.source_agent_id,source_run_id=EXCLUDED.source_run_id,input=jsonb_strip_nulls(jsonb_build_object('body',public_actions.input->'body','due_at',public_actions.input->'due_at')) || EXCLUDED.input,due_at=COALESCE(EXCLUDED.due_at,public_actions.due_at),updated_at=now() RETURNING `+actionCols, s.OwnerID, newID(), kind, in.Target, s.AgentID, s.RunID, status, raw, in.Due))
		if e != nil {
			return e
		}
		if in.Mode != "" {
			_, e = tx.Exec(ctx, `INSERT INTO reminder_keys(owner_id,reminder_key,action_id,original_due,original_mode,repeat_weekly,timezone,preference_rev) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(owner_id,reminder_key) DO UPDATE SET action_id=EXCLUDED.action_id,original_due=EXCLUDED.original_due,original_mode=EXCLUDED.original_mode,repeat_weekly=EXCLUDED.repeat_weekly,timezone=EXCLUDED.timezone,preference_rev=EXCLUDED.preference_rev`, s.OwnerID, reminderKey(in), a.ID, in.Due, in.Mode, in.RepeatWeekly, in.Timezone, rev)
			if e != nil {
				return storageError(e)
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO action_receipts(owner_id,kind,request_key,action_id,request) VALUES($1,$2,$3,$4,$5)`, s.OwnerID, kind, in.Key, a.ID, raw)
		return storageError(e)
	})
	return
}
func (r *actionRepo) GetAction(ctx context.Context, owner, kind, id string) (model.PublicAction, error) {
	return scanAction(r.db.pool.QueryRow(ctx, `SELECT `+actionCols+` FROM public_actions WHERE owner_id=$1 AND kind=$2 AND id=$3 AND EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled)`, owner, kind, id))
}
func (r *actionRepo) ListActions(ctx context.Context, owner, kind string, limit, offset int) ([]model.PublicAction, error) {
	if owner == "" || limit < 1 || limit > 100 || offset < 0 || offset > 100000 || (kind != "todo" && kind != "job" && kind != "notice") {
		return nil, model.ErrInvalidArgument
	}
	rows, e := r.db.pool.Query(ctx, `SELECT `+actionCols+` FROM public_actions WHERE owner_id=$1 AND kind=$2 AND EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled) ORDER BY updated_at DESC,id LIMIT $3 OFFSET $4`, owner, kind, limit, offset)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.PublicAction{}
	for rows.Next() {
		a, e := scanAction(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, storageError(rows.Err())
}
func (r *actionRepo) ChangeAction(ctx context.Context, s model.ActionScope, kind, id string) (a model.PublicAction, err error) {
	status, from := "complete", "open"
	if kind == "job" {
		status, from = "cancelled", "pending"
	} else if kind != "todo" {
		return a, model.ErrInvalidArgument
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := actionActor(ctx, tx, s); e != nil {
			return e
		}
		var e error
		a, e = scanAction(tx.QueryRow(ctx, `UPDATE public_actions SET status=$4,updated_at=CASE WHEN status=$4 THEN updated_at ELSE now() END WHERE owner_id=$1 AND id=$2 AND kind=$3 AND status IN ($4,$5) RETURNING `+actionCols, s.OwnerID, id, kind, status, from))
		return e
	})
	return
}
func (r *actionRepo) ClaimAction(ctx context.Context) (j model.ClaimedAction, err error) {
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var owner, id string
		e := tx.QueryRow(ctx, `SELECT owner_id,id FROM public_actions WHERE kind='job' AND status='pending' AND due_at<=now() AND EXISTS(SELECT 1 FROM users WHERE id=owner_id AND NOT disabled) ORDER BY due_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&owner, &id)
		if e != nil {
			return storageError(e)
		}
		j.OwnerID = owner
		j.PublicAction, e = scanAction(tx.QueryRow(ctx, `UPDATE public_actions SET status='running',updated_at=now() WHERE owner_id=$1 AND id=$2 RETURNING `+actionCols, owner, id))
		return e
	})
	return
}

// No ambiguous Worker/ToolCall is automatically replayed after a process crash.
func (r *actionRepo) FinishAction(ctx context.Context, j model.ClaimedAction, status, ref string) error {
	if status != "failed" && status != "succeeded" || len(ref) > 512 || len(j.Result) > 32768 || (len(j.Result) > 0 && !json.Valid(j.Result)) {
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		var locked int
		if e := tx.QueryRow(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, j.OwnerID).Scan(&locked); e != nil {
			return storageError(e)
		}
		a, e := scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+` FROM public_actions WHERE owner_id=$1 AND id=$2 AND kind='job' AND status='running' FOR UPDATE`, j.OwnerID, j.ID))
		if e != nil {
			return e
		}
		if status == "succeeded" && a.Input.Mode == "active" {
			rev, err := reminderProof(ctx, tx, model.ActionScope{OwnerID: j.OwnerID, AgentID: a.AgentID, RunID: a.RunID}, a.Input)
			if err != nil && !errors.Is(err, model.ErrInvalidArgument) {
				return err
			}
			var current bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reminder_keys WHERE owner_id=$1 AND action_id=$2 AND preference_rev IS NOT DISTINCT FROM $3::timestamptz)`, j.OwnerID, a.ID, rev).Scan(&current)
			if e != nil {
				return storageError(e)
			}
			if err != nil || !current {
				status = "cancelled"
				ref = "intent_no_longer_current"
			}
		}
		if status == "succeeded" && a.Input.Kind == "Notice" {
			var n int
			e = tx.QueryRow(ctx, `SELECT 1 FROM users WHERE id=$1 AND NOT disabled FOR SHARE`, j.OwnerID).Scan(&n)
			if e != nil {
				return storageError(e)
			}
			in := a.Input
			in.Key = "internal:" + a.ID
			in.Kind = ""
			in.Due = nil
			in.RepeatWeekly = false
			in.Timezone = ""
			raw, e := json.Marshal(in)
			if e != nil {
				return model.ErrInvalidArgument
			}
			id := newID()
			_, e = tx.Exec(ctx, `INSERT INTO public_actions(owner_id,id,kind,source_agent_id,source_run_id,status,input) VALUES($1,$2,'notice',$3,$4,'recorded',$5)`, j.OwnerID, id, a.AgentID, a.RunID, raw)
			if e != nil {
				return storageError(e)
			}
			ref = "notice:" + id
		}
		if status == "succeeded" && a.Input.RepeatWeekly {
			in := a.Input
			due, e := nextWeekly(in, time.Now().UTC())
			if e != nil {
				return e
			}
			in.Due = &due
			raw, e := json.Marshal(in)
			if e != nil {
				return model.ErrInvalidArgument
			}
			successor := newID()
			_, e = tx.Exec(ctx, `INSERT INTO public_actions(owner_id,id,kind,source_agent_id,source_run_id,status,input,due_at) VALUES($1,$2,'job',$3,$4,'pending',$5,$6)`, j.OwnerID, successor, a.AgentID, a.RunID, raw, due)
			if e == nil && in.Kind == "Notice" {
				_, e = tx.Exec(ctx, `UPDATE reminder_keys SET action_id=$3 WHERE owner_id=$1 AND action_id=$2`, j.OwnerID, a.ID, successor)
			}
			if e != nil {
				return storageError(e)
			}
		}
		_, e = tx.Exec(ctx, `UPDATE public_actions SET status=$3,result_ref=$4,updated_at=$5,result=$6 WHERE owner_id=$1 AND id=$2`, j.OwnerID, j.ID, status, ref, time.Now().UTC(), []byte(j.Result))
		return storageError(e)
	})
}
