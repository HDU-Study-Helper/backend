package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

func TestCollectOnceWithTimeoutBounds(t *testing.T) {
	for _, duration := range []time.Duration{0, -time.Second, 4*time.Hour + 1} {
		var service *Service
		if err := service.CollectOnceWithTimeout(context.Background(), duration); !errors.Is(err, contract.ErrInvalid) {
			t.Fatalf("timeout %s: %v", duration, err)
		}
	}
	adapter := &collectAdapter{}
	service, _ := collectFixture(t, adapter)
	if err := service.CollectOnceWithTimeout(context.Background(), 3*time.Hour); err != nil || adapter.calls != 4 {
		t.Fatalf("maintenance collection: calls=%d err=%v", adapter.calls, err)
	}
}

func TestCollectOnceWithTimeoutHonorsCallerCancellation(t *testing.T) {
	adapter := &collectAdapter{}
	service, _ := collectFixture(t, adapter)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.CollectOnceWithTimeout(ctx, 3*time.Hour); !errors.Is(err, context.Canceled) || adapter.calls != 0 {
		t.Fatalf("caller cancellation: calls=%d err=%v", adapter.calls, err)
	}
}
