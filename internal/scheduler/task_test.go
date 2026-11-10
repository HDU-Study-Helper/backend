package scheduler

import (
	"academic/backend/internal/model"
	"context"
	"testing"
	"time"
)

func event(t *testing.T, task *Task) {
	t.Helper()
	select {
	case <-task.Events():
	case <-time.After(time.Second):
		t.Fatal("task was not woken")
	}
}
func TestManualPauseAndIntervalLifecycle(t *testing.T) {
	task := New("refresh", "refresh", 10*time.Minute)
	if task.Trigger() {
		t.Fatal("inactive worker accepted manual run")
	}
	task.Start()
	defer task.Stop()
	event(t, task)
	if !task.Begin() {
		t.Fatal("startup scan did not begin")
	}
	if task.Trigger() {
		t.Fatal("running scan accepted duplicate")
	}
	task.Update(false, 120)
	task.Finish(true)
	state := task.Snapshot()
	if state.NextRun != nil || state.Running || state.LastError == "" || state.Runs != 1 {
		t.Fatalf("paused finish: %+v", state)
	}
	if !task.Trigger() || task.Trigger() {
		t.Fatal("manual request was lost or not coalesced")
	}
	event(t, task)
	if !task.Begin() {
		t.Fatal("manual paused scan did not begin")
	}
	task.Finish(false)
	if state = task.Snapshot(); state.NextRun != nil || state.LastError != "" {
		t.Fatalf("paused manual finish: %+v", state)
	}
	task.Update(true, 180)
	state = task.Snapshot()
	if state.NextRun == nil || time.Until(*state.NextRun) < 179*time.Second || state.IntervalSeconds != 180 {
		t.Fatalf("resume interval: %+v", state)
	}
	task.Update(false, 180)
	if task.Begin() {
		t.Fatal("paused scan began from stale timer event")
	}
	task.Stop()
	if task.Snapshot().Active || task.Trigger() {
		t.Fatal("stopped timer active")
	}
}
func TestRunSerialAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := New("refresh", "refresh", 10*time.Minute)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- task.Run(ctx, func(ctx context.Context) bool {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return false
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scan not started")
	}
	for i := 0; i < 10; i++ {
		if task.Trigger() {
			t.Fatal("concurrent run queued")
		}
	}
	task.Update(false, 600)
	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	state := task.Snapshot()
	if state.Active || state.Running || state.Runs != 1 {
		t.Fatalf("worker state: %+v", state)
	}
}

func TestRoundHistoryIsBoundedAndSnapshotsDoNotLeak(t *testing.T) {
	task := New("refresh", "refresh", time.Minute)
	for i := 0; i < 12; i++ {
		ctx, report := model.WithRefreshReport(context.Background())
		model.InitRefreshSource(ctx, "dws", true)
		model.RefreshCommitted(ctx, "dws", i, 0, 0)
		model.RefreshAttempt(ctx, "dws", "")
		task.report = report
		task.Finish(false)
	}
	snapshot := task.Snapshot()
	if len(snapshot.History) != 10 || snapshot.History[0].Results[0].Created != 11 || snapshot.History[9].Results[0].Created != 2 {
		t.Fatalf("history not bounded: %+v", snapshot)
	}
	snapshot.Results[0].Created = 999
	if task.Snapshot().Results[0].Created == 999 {
		t.Fatal("caller mutated snapshot")
	}
}
