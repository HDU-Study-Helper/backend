package dbbridge

import (
	"academic/backend/internal/model"
	"context"
)

// ListPollTargets is deliberately separate from owner-scoped user operations.
func (r *dws) ListPollTargets(ctx context.Context, limit int, afterID string) ([]model.DWSConnection, error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidArgument
	}
	rows, e := r.db.pool.Query(ctx, `SELECT c.id,c.user_id,c.profile,c.corp_id,c.upstream_user_id,c.mode,c.enabled,c.state,c.created_at,c.updated_at FROM dws_connections c JOIN users u ON u.id=c.user_id WHERE c.enabled AND c.state='active' AND NOT u.disabled AND c.id>$1 ORDER BY c.id LIMIT $2`, afterID, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.DWSConnection{}
	for rows.Next() {
		c, e := scanConn(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, storageError(rows.Err())
}
