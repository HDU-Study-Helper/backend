package dbbridge

import (
	"context"

	"academic/backend/internal/model"
)

var _ model.ControlRoundHeartbeater = (*controls)(nil)

// HeartbeatRound renews the named refreshing round only when its owner matches.
// Collection must not interrupt or renew unrelated rounds, runs or batches.
func (r *controls) HeartbeatRound(ctx context.Context, round, instance string) error {
	if round == "" || instance == "" {
		return model.ErrInvalidArgument
	}
	tag, err := r.db.pool.Exec(ctx, `UPDATE control_rounds SET heartbeat=now() WHERE id=$1 AND lease_owner=$2 AND status='refreshing'`, round, instance)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return model.ErrConflict
	}
	return nil
}
