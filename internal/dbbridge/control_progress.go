package dbbridge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

var controlExecutionRoundID = regexp.MustCompile(`^(?:replay_)?[a-f0-9]{32}$`)

// ControlRoundProgress reads complete aggregates, never the bounded admin overview.
type ControlRoundProgress struct {
	RoundID      string           `json:"round_id"`
	At           time.Time        `json:"at"`
	RoundStatus  string           `json:"round_status"`
	Batches      map[string]int64 `json:"batches"`
	Items        map[string]int64 `json:"items"`
	Runs         map[string]int64 `json:"runs"`
	Artifacts    int64            `json:"artifacts"`
	Total        int64            `json:"total_batches"`
	Unsealed     int64            `json:"unsealed_events"`
	OtherQueued  int64            `json:"other_queued"`
	OtherRunning int64            `json:"other_running"`
}

func (p ControlRoundProgress) Preflight() error {
	if p.Total == 0 {
		return fmt.Errorf("specified round has no batches")
	}
	if p.OtherQueued != 0 || p.OtherRunning != 0 || p.Batches["running"] != 0 {
		return fmt.Errorf("queue guard rejected: another queued batch or any running batch exists")
	}
	if p.Unsealed != 0 || p.RoundStatus == "refreshing" {
		return fmt.Errorf("specified round is not completely sealed")
	}
	if p.Batches["pending"] != 0 {
		return fmt.Errorf("specified round contains pending batches; activate explicitly before execution")
	}
	return p.validateStatuses()
}
func (p ControlRoundProgress) validateStatuses() error {
	for status := range p.Batches {
		switch status {
		case "pending", "queued", "running", "completed", "succeeded", "partial", "ignored", "failed", "cancelled", "interrupted", "blocked":
		default:
			return fmt.Errorf("unknown batch status %q", status)
		}
	}
	return nil
}
func (p ControlRoundProgress) Done() bool {
	return p.Total > 0 && p.Unsealed == 0 && p.Batches["queued"] == 0 && p.Batches["running"] == 0 && p.Batches["pending"] == 0 && p.validateStatuses() == nil
}

func (d *DB) ControlRoundProgress(ctx context.Context, round string) (out ControlRoundProgress, err error) {
	if !controlExecutionRoundID.MatchString(round) {
		return out, model.ErrInvalidArgument
	}
	out = ControlRoundProgress{RoundID: round, At: time.Now().UTC(), Batches: map[string]int64{}, Items: map[string]int64{}, Runs: map[string]int64{}}
	tx, e := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, storageError(e)
	}
	defer tx.Rollback(ctx)
	if e = tx.QueryRow(ctx, `SELECT status FROM control_rounds WHERE id=$1`, round).Scan(&out.RoundStatus); e != nil {
		return out, storageError(e)
	}
	read := func(sql string, target map[string]int64) error {
		rows, e := tx.Query(ctx, sql, round)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var count int64
			if e = rows.Scan(&key, &count); e != nil {
				return e
			}
			target[key] = count
		}
		return rows.Err()
	}
	if e = read(`SELECT status,count(*) FROM control_batches WHERE round_id=$1 GROUP BY status`, out.Batches); e != nil {
		return out, storageError(e)
	}
	for _, count := range out.Batches {
		out.Total += count
	}
	if e = read(`SELECT status,count(*) FROM control_items WHERE round_id=$1 GROUP BY status`, out.Items); e != nil {
		return out, storageError(e)
	}
	if e = read(`SELECT r.state,count(*) FROM control_runs r JOIN control_batches b ON b.id=r.batch_id WHERE b.round_id=$1 GROUP BY r.state`, out.Runs); e != nil {
		return out, storageError(e)
	}
	e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM control_artifacts WHERE round_id=$1),(SELECT count(*) FROM control_events WHERE round_id=$1 AND batch_id IS NULL),(SELECT count(*) FROM control_batches WHERE round_id<>$1 AND status='queued'),(SELECT count(*) FROM control_batches WHERE round_id<>$1 AND status='running')`, round).Scan(&out.Artifacts, &out.Unsealed, &out.OtherQueued, &out.OtherRunning)
	if e != nil {
		return out, storageError(e)
	}
	return out, storageError(tx.Commit(ctx))
}

// ControlRoundRepository prevents a one-round executor from claiming/sealing or
// performing stale-work recovery in any other round. Telemetry and item/artifact
// capabilities remain the original dbbridge repository methods.
func (d *DB) ControlRoundRepository(round string) (model.ControlRepository, error) {
	if !controlExecutionRoundID.MatchString(round) {
		return nil, model.ErrInvalidArgument
	}
	return &roundControls{controls: &controls{d}, round: round}, nil
}

// PauseControlRoundQueue stops only not-yet-claimed queued batches. The host
// callback must durably persist the complete pre-change snapshot; an error rolls
// back all state changes. It does not cancel running work or reactivate anything.
func (d *DB) PauseControlRoundQueue(ctx context.Context, round string, beforeCommit func([]model.ControlBatch) error) (int, error) {
	if !controlExecutionRoundID.MatchString(round) || beforeCommit == nil {
		return 0, model.ErrInvalidArgument
	}
	count := 0
	err := d.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE control_batches IN EXCLUSIVE MODE`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM control_rounds WHERE id=$1)`, round).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return model.ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT `+batchCols+` FROM control_batches WHERE round_id=$1 AND status='queued' ORDER BY id FOR UPDATE`, round)
		if err != nil {
			return err
		}
		backup := []model.ControlBatch{}
		for rows.Next() {
			batch, err := scanBatch(rows)
			if err != nil {
				rows.Close()
				return err
			}
			backup = append(backup, batch)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if err = beforeCommit(backup); err != nil {
			return fmt.Errorf("drain backup failed; queue unchanged: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE control_batches SET status='pending',error_code='DEPLOYMENT_DRAIN',updated_at=now() WHERE round_id=$1 AND status='queued'`, round)
		if err != nil {
			return err
		}
		count = int(tag.RowsAffected())
		if count != len(backup) {
			return model.ErrConflict
		}
		return nil
	})
	return count, err
}

type roundControls struct {
	*controls
	round string
}

func (r *roundControls) Claim(ctx context.Context, instance string) (b model.ControlBatch, err error) {
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT id FROM control_batches WHERE round_id=$1 AND status='queued' ORDER BY CASE WHEN stage='trigger' THEN 0 ELSE 1 END,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, r.round).Scan(&id); e != nil {
			return e
		}
		var e error
		b, e = scanBatch(tx.QueryRow(ctx, `UPDATE control_batches SET status='running',attempt=attempt+1,heartbeat=now(),lease_owner=$2,updated_at=now() WHERE id=$1 RETURNING `+batchCols, id, instance))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return b, model.ErrNotFound
	}
	return b, storageError(err)
}
func (r *roundControls) Seal(ctx context.Context) error {
	var n int
	if e := r.db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE round_id=$1 AND batch_id IS NULL`, r.round).Scan(&n); e != nil {
		return storageError(e)
	}
	if n != 0 {
		return model.ErrConflict
	}
	return nil
}
func (r *roundControls) Heartbeat(ctx context.Context, instance string) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE control_runs r SET updated_at=now() FROM control_batches b WHERE b.id=r.batch_id AND b.round_id=$1 AND r.lease_owner=$2 AND r.state NOT IN ('succeeded','failed','cancelled','interrupted')`, r.round, instance); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE control_batches SET heartbeat=now() WHERE round_id=$1 AND status='running' AND lease_owner=$2`, r.round, instance)
		return e
	})
}
