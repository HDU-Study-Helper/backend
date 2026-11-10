package dbbridge

import (
	f "academic/agent/framework"
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

// Dedicated connection: waiting runs do not occupy the repository pool.
// Disconnecting releases the session lock, including on a process crash.
func (r *agents) LockWorkflow(ctx context.Context, s f.Scope) (func(), error) {
	cfg := r.db.pool.Config().ConnConfig.Copy()
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return nil, f.Fail("INTERNAL_ERROR", "workflow lock connection unavailable")
	}
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,730022412))`, s.OwnerID+":"+s.WorkflowID); e != nil {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = conn.Close(c)
		return nil, e
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = conn.Close(c)
	}, nil
}
