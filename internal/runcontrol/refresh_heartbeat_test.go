package runcontrol

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"academic/backend/internal/model"
)

type scopedRefreshHeartbeat struct {
	model.ControlRepository
	calls chan [2]string
	exits chan struct{}
	block bool
}

func (r *scopedRefreshHeartbeat) HeartbeatRound(ctx context.Context, round, instance string) error {
	r.calls <- [2]string{round, instance}
	if r.block {
		<-ctx.Done()
		close(r.exits)
		return ctx.Err()
	}
	return nil
}

func TestRefreshHeartbeatUsesOwnedRoundAndJoinsOnStop(t *testing.T) {
	repo := &scopedRefreshHeartbeat{calls: make(chan [2]string, 1), exits: make(chan struct{}), block: true}
	s := &Service{Store: repo, instance: "collection-owner"}
	// Exercise the production method: it must use StartRefresh's private owner.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := s.KeepRefreshAlive(ctx, "collection-round")
	defer stop()
	select {
	case got := <-repo.calls:
		if got != [2]string{"collection-round", "collection-owner"} {
			t.Fatalf("heartbeat has wrong round ownership: %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no collection heartbeat within 10 seconds")
	}
	stop()
	select {
	case <-repo.exits:
	default:
		t.Fatal("stop returned before the in-flight heartbeat exited")
	}
	// stop must also be safe when deferred after an explicit stop.
	stop()
}

type legacyRefreshHeartbeat struct {
	model.ControlRepository
	calls chan string
	count atomic.Int32
}

func (r *legacyRefreshHeartbeat) Heartbeat(_ context.Context, instance string) error {
	r.count.Add(1)
	r.calls <- instance
	return nil
}

func TestRefreshHeartbeatLegacyFallbackAndCancellation(t *testing.T) {
	repo := &legacyRefreshHeartbeat{calls: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	stop := keepRefreshAlive(ctx, repo, "round", "legacy-owner", time.Millisecond)
	defer stop()
	select {
	case owner := <-repo.calls:
		if owner != "legacy-owner" {
			t.Fatalf("wrong heartbeat owner: %s", owner)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy heartbeat not called")
	}
	cancel()
	stop()
	count := repo.count.Load()
	if count == 0 {
		t.Fatal("heartbeat never ran")
	}
	// Once stop joins, finalization can run without a concurrent heartbeat.
	stop()
	if repo.count.Load() != count {
		t.Fatal("heartbeat continued after stop")
	}
}
