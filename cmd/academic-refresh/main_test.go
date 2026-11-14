package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"academic/backend/internal/model"
)

type collectionControl struct {
	model.ControlRepository
	owner     string
	round     string
	beat      chan [2]string
	beatDone  chan struct{}
	mu        sync.Mutex
	inBeat    bool
	finished  bool
	sealed    bool
	failed    bool
	finishErr error
	startErr  error
}

func (r *collectionControl) StartRound(_ context.Context, owner string) (string, error) {
	r.owner = owner
	return r.round, r.startErr
}
func (r *collectionControl) HeartbeatRound(ctx context.Context, round, owner string) error {
	r.mu.Lock()
	r.inBeat = true
	r.mu.Unlock()
	r.beat <- [2]string{round, owner}
	<-ctx.Done()
	r.mu.Lock()
	r.inBeat = false
	r.mu.Unlock()
	close(r.beatDone)
	return ctx.Err()
}
func (r *collectionControl) FinishRound(ctx context.Context, round string, _ []model.RefreshResult, failed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inBeat || ctx.Err() != nil || round != r.round {
		return errors.New("invalid finalization lifecycle")
	}
	r.finished = true
	r.failed = failed
	return r.finishErr
}
func (r *collectionControl) Seal(context.Context) error {
	r.sealed = true
	return nil
}

func TestCollectRoundHeartbeatAndCanceledCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &collectionControl{round: "owned-round", beat: make(chan [2]string, 1), beatDone: make(chan struct{})}
	round, err := collectRound(ctx, repo, func(ctx context.Context) error {
		if model.ControlRoundID(ctx) != repo.round {
			t.Fatal("collection context lost round")
		}
		select {
		case got := <-repo.beat:
			if got != [2]string{repo.round, repo.owner} {
				t.Fatalf("heartbeat ownership differs from StartRound: %v", got)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("missing collection heartbeat")
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || round != repo.round || !repo.finished || !repo.sealed || !repo.failed {
		t.Fatalf("canceled collection was not finalized: round=%s err=%v finished=%v sealed=%v failed=%v", round, err, repo.finished, repo.sealed, repo.failed)
	}
	select {
	case <-repo.beatDone:
	default:
		t.Fatal("collection returned before heartbeat stopped")
	}
}

func TestCollectRoundUniqueOwnersAndFailureCleanup(t *testing.T) {
	var owners []string
	failure := errors.New("collection failed")
	finishFailure := errors.New("finish failed")
	for i := 0; i < 2; i++ {
		repo := &collectionControl{round: "round", finishErr: finishFailure}
		_, err := collectRound(context.Background(), repo, func(context.Context) error { return failure })
		if !errors.Is(err, failure) || !errors.Is(err, finishFailure) || !repo.finished || !repo.sealed || !repo.failed {
			t.Fatalf("errors or cleanup lost: %v", err)
		}
		if !strings.HasPrefix(repo.owner, "academic-refresh-") {
			t.Fatalf("missing collector owner: %q", repo.owner)
		}
		owners = append(owners, repo.owner)
	}
	if owners[0] == owners[1] {
		t.Fatal("independent collections reused an owner")
	}
}

func TestCollectionOptions(t *testing.T) {
	for _, test := range []struct {
		name, timeout, interval string
		wantTimeout, wantGap    time.Duration
		invalid                 bool
	}{
		{name: "defaults", wantTimeout: 30 * time.Minute, wantGap: 3 * time.Second},
		{name: "maintenance", timeout: "3h", interval: "3s", wantTimeout: 3 * time.Hour, wantGap: 3 * time.Second},
		{name: "upper bounds", timeout: "4h", interval: "30s", wantTimeout: 4 * time.Hour, wantGap: 30 * time.Second},
		{name: "lower bounds", timeout: "1ns", interval: "500ms", wantTimeout: 1, wantGap: 500 * time.Millisecond},
		{name: "invalid timeout", timeout: "later", invalid: true},
		{name: "zero timeout", timeout: "0", invalid: true},
		{name: "negative timeout", timeout: "-1s", invalid: true},
		{name: "excess timeout", timeout: "4h1ns", invalid: true},
		{name: "invalid interval", interval: "fast", invalid: true},
		{name: "disabled interval", interval: "0", invalid: true},
		{name: "negative interval", interval: "-1s", invalid: true},
		{name: "short interval", interval: "499ms", invalid: true},
		{name: "excess interval", interval: "30s1ns", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{"ACADEMIC_COLLECTION_TIMEOUT": test.timeout, "ACADEMIC_REQUEST_INTERVAL": test.interval}
			got, err := parseCollectionOptions(func(name string) string { return values[name] })
			if test.invalid {
				if err == nil {
					t.Fatal("accepted invalid collection configuration")
				}
				return
			}
			if err != nil || got.timeout != test.wantTimeout || got.interval != test.wantGap {
				t.Fatalf("options = %+v, %v", got, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPacedTransportCancellationWhileWaiting(t *testing.T) {
	for _, waitingOn := range []string{"interval", "active request"} {
		t.Run(waitingOn, func(t *testing.T) {
			called := false
			transport := newPacedTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return nil, errors.New("unexpected request")
			}), time.Hour)
			if waitingOn == "interval" {
				transport.next = time.Now().Add(time.Hour)
			} else {
				transport.lane <- struct{}{}
				defer func() { <-transport.lane }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://fixture.invalid/read", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.RoundTrip(req)
			if !errors.Is(err, context.DeadlineExceeded) || called {
				t.Fatalf("wait cancellation: err=%v called=%v", err, called)
			}
		})
	}
}

func TestPacedTransportSeparatesRequestsAndDoesNotRetry(t *testing.T) {
	var calls []time.Time
	failure := errors.New("upstream unavailable")
	transport := newPacedTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls = append(calls, time.Now())
		return nil, failure
	}), 20*time.Millisecond)
	for _, host := range []string{"one.invalid", "two.invalid"} {
		req, err := http.NewRequest(http.MethodGet, "https://"+host+"/read", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = transport.RoundTrip(req); !errors.Is(err, failure) {
			t.Fatalf("lost upstream failure: %v", err)
		}
	}
	if len(calls) != 2 || calls[1].Sub(calls[0]) < 20*time.Millisecond {
		t.Fatalf("requests retried or gap lost: %v", calls)
	}
}

func TestCollectRoundStartFailureDoesNotCollectOrFinalize(t *testing.T) {
	failure := errors.New("start failed")
	repo := &collectionControl{startErr: failure}
	_, err := collectRound(context.Background(), repo, func(context.Context) error {
		t.Fatal("collected without a round")
		return nil
	})
	if !errors.Is(err, failure) || repo.finished || repo.sealed {
		t.Fatalf("start failure lifecycle: %v", err)
	}
}
