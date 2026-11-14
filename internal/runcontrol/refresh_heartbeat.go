package runcontrol

import (
	"context"
	"log/slog"
	"time"

	"academic/backend/internal/model"
)

// KeepRefreshAlive renews a collection round independently of the Agent worker.
// instance must be the same owner passed to StartRound. Call stop before
// FinishRound; it cancels an in-flight heartbeat and waits for the goroutine.
func KeepRefreshAlive(ctx context.Context, repo model.ControlRepository, round, instance string) (stop func()) {
	return keepRefreshAlive(ctx, repo, round, instance, 5*time.Second)
}

// KeepRefreshAlive uses the same instance as this service's StartRefresh.
func (s *Service) KeepRefreshAlive(ctx context.Context, round string) (stop func()) {
	return KeepRefreshAlive(ctx, s.Store, round, s.instance)
}

func keepRefreshAlive(ctx context.Context, repo model.ControlRepository, round, instance string, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beat, end := context.WithTimeout(ctx, 5*time.Second)
				var err error
				if scoped, ok := repo.(model.ControlRoundHeartbeater); ok {
					err = scoped.HeartbeatRound(beat, round, instance)
				} else {
					// Preserve compatibility with repositories that only implement
					// the original instance-scoped heartbeat.
					err = repo.Heartbeat(beat, instance)
				}
				end()
				if err != nil && ctx.Err() == nil {
					slog.Warn("collection round heartbeat failed", "round_id", round)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
