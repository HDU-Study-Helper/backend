package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type controls struct{ db *DB }

func (d *DB) Control() model.ControlRepository { return &controls{d} }
func sensitive(k string) bool {
	k = strings.ToLower(k)
	for _, v := range []string{"password", "cookie", "token", "secret", "authorization", "api_key", "credential", "session"} {
		if strings.Contains(k, v) {
			return true
		}
	}
	return false
}
func safeSnapshot(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var data any
	if json.Unmarshal(raw, &data) != nil {
		data = string(raw)
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if sensitive(k) {
					delete(x, k)
				} else {
					x[k] = clean(val)
				}
			}
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
		case string:
			if u, e := url.Parse(x); e == nil && u.Host != "" {
				q := u.Query()
				for k := range q {
					if sensitive(k) {
						q.Del(k)
					}
				}
				u.RawQuery = q.Encode()
				return u.String()
			}
		}
		return v
	}
	out, _ := json.Marshal(clean(data))
	return out
}

// Capture belongs to the same transaction as the source record and checkpoint.
func captureControl(ctx context.Context, tx pgx.Tx, owner, source string, changes []model.ControlChange) error {
	if len(changes) == 0 {
		return nil
	}
	if e := firstLock(ctx, tx, owner); e != nil {
		return e
	}
	round := model.ControlRoundID(ctx)
	if round == "" {
		round = newID()
		if _, e := tx.Exec(ctx, `INSERT INTO control_rounds(id,status,finished_at)VALUES($1,'completed',now())`, round); e != nil {
			return e
		}
	}
	for _, c := range changes {
		c.ID = newID()
		c.Source = source
		c.Before = safeSnapshot(c.Before)
		c.After = safeSnapshot(c.After)
		raw, e := json.Marshal(c)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO control_events(id,round_id,owner_id,source,payload,after_first_click)VALUES($1,$2,$3,$4,$5,EXISTS(SELECT 1 FROM first_runs WHERE owner_id=$3))`, c.ID, round, owner, source, raw); e != nil {
			return e
		}
	}
	return nil
}
func (r *controls) StartRound(ctx context.Context, instance string) (string, error) {
	id := newID()
	_, e := r.db.pool.Exec(ctx, `INSERT INTO control_rounds(id,status,lease_owner)VALUES($1,'refreshing',$2)`, id, instance)
	return id, storageError(e)
}
func (r *controls) FinishRound(ctx context.Context, id string, results []model.RefreshResult, failed bool) error {
	raw, _ := json.Marshal(results)
	status := "completed"
	if failed {
		status = "partial_failure"
	}
	_, e := r.db.pool.Exec(ctx, `UPDATE control_rounds SET status=$2,finished_at=now(),results=$3 WHERE id=$1`, id, status, raw)
	return storageError(e)
}
func (r *controls) Seal(ctx context.Context) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		// The advisory lock serializes assignment; every course is sealed atomically.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(730022411)`); e != nil {
			return e
		}
		if e := sealControlBatches(ctx, tx); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE control_batches b SET status='queued',updated_at=now() FROM first_runs f JOIN control_batches first_batch ON first_batch.id=f.batch_id WHERE b.owner_id=f.owner_id AND b.after_first_click AND b.status='pending' AND first_batch.status='succeeded'`); e != nil {
			return e
		}
		return seedControlItems(ctx, tx)
	})
}

const batchCols = `id,round_id,owner_id,source,status,stage,change_count,attempt,changes,trigger_output,filter_output,error_code,created_at,updated_at,batch_scope`

func scanBatch(row pgx.Row) (b model.ControlBatch, e error) {
	var raw, scope []byte
	e = row.Scan(&b.ID, &b.RoundID, &b.OwnerID, &b.Source, &b.Status, &b.Stage, &b.Count, &b.Attempt, &raw, &b.Trigger, &b.Filter, &b.Error, &b.CreatedAt, &b.UpdatedAt, &scope)
	if e == nil {
		e = json.Unmarshal(raw, &b.Changes)
	}
	if e == nil && len(scope) > 0 {
		e = json.Unmarshal(scope, &b.Scope)
	}
	return b, storageError(e)
}
func (r *controls) GetBatch(ctx context.Context, id string) (model.ControlBatch, error) {
	return scanBatch(r.db.pool.QueryRow(ctx, `SELECT `+batchCols+` FROM control_batches WHERE id=$1`, id))
}
func (r *controls) Activate(ctx context.Context, round, source, id string, retry bool) (int, error) {
	if round == "" && id == "" {
		return 0, model.ErrInvalidArgument
	}
	tag, e := r.db.pool.Exec(ctx, `UPDATE control_batches b SET status='queued',error_code='',updated_at=now() WHERE ($1='' OR round_id=$1) AND ($2='' OR source=$2) AND ($3='' OR id=$3) AND (status='pending' OR ($4 AND status IN ('failed','cancelled','interrupted') AND NOT EXISTS(SELECT 1 FROM control_runs cr WHERE cr.batch_id=b.id AND (cr.workflow_id<>'' OR COALESCE(cr.record->>'parent_run_id','')<>''))))`, round, source, id, retry)
	return int(tag.RowsAffected()), storageError(e)
}
func (r *controls) Claim(ctx context.Context, instance string) (model.ControlBatch, error) {
	var b model.ControlBatch
	e := r.db.transaction(ctx, func(tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT id FROM control_batches WHERE status='queued' ORDER BY CASE WHEN stage='trigger' THEN 0 ELSE 1 END,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id); e != nil {
			return e
		}
		var e error
		b, e = scanBatch(tx.QueryRow(ctx, `UPDATE control_batches SET status='running',attempt=attempt+1,heartbeat=now(),lease_owner=$2,updated_at=now() WHERE id=$1 RETURNING `+batchCols, id, instance))
		return e
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return b, model.ErrNotFound
	}
	return b, storageError(e)
}
func (r *controls) Heartbeat(ctx context.Context, instance string) error {
	if _, e := r.db.pool.Exec(ctx, `UPDATE control_runs SET updated_at=now() WHERE lease_owner=$1 AND state NOT IN ('succeeded','failed','cancelled','interrupted')`, instance); e != nil {
		return storageError(e)
	}
	if _, e := r.db.pool.Exec(ctx, `UPDATE control_runs SET state='interrupted',record=jsonb_set(record,'{state}','"interrupted"'::jsonb) WHERE updated_at<now()-interval '45 seconds' AND state NOT IN ('succeeded','failed','cancelled','interrupted')`); e != nil {
		return storageError(e)
	}

	if _, e := r.db.pool.Exec(ctx, `UPDATE control_rounds SET heartbeat=now() WHERE status='refreshing' AND lease_owner=$1`, instance); e != nil {
		return storageError(e)
	}
	if _, e := r.db.pool.Exec(ctx, `UPDATE control_rounds SET status='interrupted',finished_at=now() WHERE status='refreshing' AND heartbeat<now()-interval '45 seconds'`); e != nil {
		return storageError(e)
	}

	_, e := r.db.pool.Exec(ctx, `UPDATE control_batches SET heartbeat=now() WHERE status='running' AND lease_owner=$1`, instance)
	if e != nil {
		return storageError(e)
	}
	_, e = r.db.pool.Exec(ctx, `WITH stale AS (UPDATE control_batches SET status='interrupted',error_code='PROCESS_INTERRUPTED_REVIEW_BEFORE_RETRY',updated_at=now() WHERE status='running' AND heartbeat<now()-interval '45 seconds' RETURNING id) UPDATE control_runs SET state='interrupted',record=jsonb_set(record,'{state}','"interrupted"'::jsonb),updated_at=now() WHERE batch_id IN (SELECT id FROM stale) AND state NOT IN ('succeeded','failed','cancelled')`)
	return storageError(e)
}
func (r *controls) SetBatch(ctx context.Context, b model.ControlBatch) error {
	if len(b.Trigger) == 0 {
		b.Trigger = json.RawMessage(`{}`)
	}
	if len(b.Filter) == 0 {
		b.Filter = json.RawMessage(`{}`)
	}
	tag, e := r.db.pool.Exec(ctx, `UPDATE control_batches SET status=$2,stage=$3,trigger_output=$4,filter_output=$5,error_code=$6,updated_at=now() WHERE id=$1 AND ((status='running' AND attempt=$7 AND $7>0) OR (status='queued' AND $2='cancelled' AND attempt=$7))`, b.ID, b.Status, b.Stage, b.Trigger, b.Filter, b.Error, b.Attempt)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrConflict
	}
	return nil
}
func (r *controls) SaveRun(ctx context.Context, v model.ControlRun) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = r.db.pool.Exec(ctx, `INSERT INTO control_runs(run_id,batch_id,owner_id,agent_id,workflow_id,state,record,lease_owner)VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(run_id)DO UPDATE SET state=EXCLUDED.state,record=EXCLUDED.record,updated_at=now()`, v.ID, v.BatchID, v.OwnerID, v.AgentID, v.WorkflowID, v.State, raw, v.LeaseOwner)
	return storageError(e)
}
func (r *controls) Overview(ctx context.Context) (out model.ControlOverview, err error) {
	out = model.ControlOverview{Mode: "manual", Rounds: []model.ControlRound{}, Batches: []model.ControlBatch{}, Runs: []model.ControlRun{}, Counts: map[string]int{}}
	rows, e := r.db.pool.Query(ctx, `SELECT id,status,started_at,finished_at,results FROM control_rounds ORDER BY started_at DESC LIMIT 20`)
	if e != nil {
		return out, storageError(e)
	}
	for rows.Next() {
		var v model.ControlRound
		if e = rows.Scan(&v.ID, &v.Status, &v.StartedAt, &v.FinishedAt, &v.Results); e != nil {
			break
		}
		out.Rounds = append(out.Rounds, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = r.db.pool.Query(ctx, `SELECT `+batchCols+` FROM control_batches ORDER BY created_at DESC LIMIT 150`)
	if e != nil {
		return out, storageError(e)
	}
	for rows.Next() {
		v, er := scanBatch(rows)
		if er != nil {
			rows.Close()
			return out, er
		}
		v.Changes = nil
		out.Batches = append(out.Batches, v)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	rows, e = r.db.pool.Query(ctx, `SELECT record FROM control_runs ORDER BY updated_at DESC LIMIT 100`)
	if e != nil {
		return out, storageError(e)
	}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			break
		}
		var v model.ControlRun
		if e = json.Unmarshal(raw, &v); e != nil {
			break
		}
		v.Output = ""
		out.Runs = append(out.Runs, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = r.db.pool.Query(ctx, `SELECT status,count(*) FROM control_batches GROUP BY status`)
	if e != nil {
		return out, storageError(e)
	}
	for rows.Next() {
		var s string
		var n int
		if e = rows.Scan(&s, &n); e != nil {
			break
		}
		out.Counts[s] = n
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, storageError(e)
	}
	out.Items, e = r.ListItems(ctx, model.ControlListQuery{Limit: 100})
	if e != nil {
		return out, e
	}
	out.Artifacts, e = r.ListArtifacts(ctx, model.ControlListQuery{Limit: 100})
	if e != nil {
		return out, e
	}
	out.ItemCounts, e = r.ItemCounts(ctx, "")
	return out, storageError(e)
}

func (r *controls) GetRun(ctx context.Context, id string) (v model.ControlRun, err error) {
	var raw []byte
	err = r.db.pool.QueryRow(ctx, `SELECT record FROM control_runs WHERE run_id=$1`, id).Scan(&raw)
	if err != nil {
		return v, storageError(err)
	}
	err = json.Unmarshal(raw, &v)
	return
}
func (r *controls) BatchRuns(ctx context.Context, id string) (out []model.ControlRun, err error) {
	out = []model.ControlRun{}
	rows, e := r.db.pool.Query(ctx, `SELECT record FROM control_runs WHERE batch_id=$1 ORDER BY updated_at`, id)
	if e != nil {
		return out, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return out, e
		}
		var v model.ControlRun
		if e = json.Unmarshal(raw, &v); e != nil {
			return out, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
