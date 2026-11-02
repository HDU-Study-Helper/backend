package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"github.com/jackc/pgx/v5"
)

// ListPendingJobs includes interrupted running jobs. Reacquiring the exclusive
// connection lease is required before resuming them.
func (r *dws) ListPendingJobs(ctx context.Context, s model.DWSScope, limit int) ([]model.SyncJob, error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidArgument
	}
	if _, e := r.GetConnection(ctx, s); e != nil {
		return nil, e
	}
	rows, e := r.db.pool.Query(ctx, `SELECT id,group_id,state,error_code,created_at,updated_at FROM dws_jobs WHERE user_id=$1 AND connection_id=$2 AND state IN ('queued','running') ORDER BY created_at,id LIMIT $3`, s.UserID, s.ConnectionID, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.SyncJob{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, storageError(rows.Err())
}

// FailLeasedJob is the only failure path for executing workers. A stale worker
// cannot change a job after another worker acquires a newer connection token.
func (r *dws) FailLeasedJob(ctx context.Context, l model.Lease, id, code string) error {
	switch code {
	case "upstream_unavailable", "auth_expired", "auth_required", "conflict", "budget_exceeded", "parse_failed", "cancelled", "internal", "lease_lost", "unsupported", "invalid_argument", "not_found", "rate_limited":
	default:
		return model.ErrInvalidArgument
	}
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE dws_jobs SET state='failed',error_code=$5,updated_at=clock_timestamp() WHERE user_id=$1 AND connection_id=$2 AND id=$3 AND ($4='' OR group_id=$4) AND state IN ('queued','running')`, l.Scope.UserID, l.Scope.ConnectionID, id, l.GroupID, code)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() == 0 {
			return model.ErrNotFound
		}
		return nil
	})
}

func (r *dws) StartJob(ctx context.Context, l model.Lease, id string) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := checkLease(ctx, tx, l); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE dws_jobs SET state='running',updated_at=now() WHERE user_id=$1 AND connection_id=$2 AND id=$3 AND ($4='' OR group_id=$4) AND state IN ('queued','running') AND EXISTS(SELECT 1 FROM dws_groups g WHERE (g.user_id,g.connection_id,g.id)=(dws_jobs.user_id,dws_jobs.connection_id,dws_jobs.group_id))`, l.Scope.UserID, l.Scope.ConnectionID, id, l.GroupID)
		if e != nil {
			return storageError(e)
		}
		if tag.RowsAffected() == 0 {
			return model.ErrNotFound
		}
		return nil
	})
}
