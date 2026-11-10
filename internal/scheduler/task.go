// Package scheduler controls existing serial polling workers; it starts no goroutines.
package scheduler

import (
	"academic/backend/internal/model"
	"context"
	"sync"
	"time"
)

type Round struct {
	FinishedAt time.Time             `json:"finished_at"`
	Results    []model.RefreshResult `json:"results"`
	Failed     bool                  `json:"failed"`
}
type State struct {
	Results         []model.RefreshResult `json:"results"`
	History         []Round               `json:"history"`
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Enabled         bool                  `json:"enabled"`
	Active          bool                  `json:"active"`
	Running         bool                  `json:"running"`
	Pending         bool                  `json:"pending"`
	IntervalSeconds int                   `json:"interval_seconds"`
	NextRun         *time.Time            `json:"next_run,omitempty"`
	LastStarted     *time.Time            `json:"last_started,omitempty"`
	LastFinished    *time.Time            `json:"last_finished,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	Runs            int                   `json:"runs"`
}
type Task struct {
	mu     sync.Mutex
	state  State
	timer  *time.Timer
	manual bool
	report *model.RefreshReport
}

func New(id, name string, interval time.Duration) *Task {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &Task{state: State{ID: id, Name: name, Enabled: true, IntervalSeconds: int(interval / time.Second)}, timer: timer}
}
func (t *Task) Snapshot() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.state
	out.Results = append([]model.RefreshResult{}, t.state.Results...)
	if out.Running && t.report != nil {
		out.Results = t.report.Snapshot()
	}
	out.History = append([]Round{}, t.state.History...)
	for i := range out.History {
		out.History[i].Results = append([]model.RefreshResult{}, out.History[i].Results...)
	}
	return out
}
func (t *Task) Events() <-chan time.Time { return t.timer.C }
func (t *Task) arm(delay time.Duration) {
	next := time.Now().Add(delay).UTC()
	t.state.NextRun = &next
	t.timer.Reset(delay)
}
func (t *Task) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.Active = true
	if t.state.Enabled || t.manual {
		t.arm(0)
	}
}
func (t *Task) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.Active = false
	t.state.Pending = false
	t.manual = false
	t.state.NextRun = nil
	t.timer.Stop()
}
func (t *Task) Update(enabled bool, seconds int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.Enabled = enabled
	t.state.IntervalSeconds = seconds
	t.timer.Stop()
	t.state.NextRun = nil
	if t.state.Active && !t.state.Running && (enabled || t.manual) {
		if t.manual {
			t.arm(0)
		} else {
			t.arm(time.Duration(seconds) * time.Second)
		}
	}
}

// Trigger coalesces repeated requests. A running scan is never overlapped or queued again.
func (t *Task) Trigger() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.state.Active || t.state.Running || t.state.Pending {
		return false
	}
	t.manual = true
	t.state.Pending = true
	t.arm(0)
	return true
}
func (t *Task) Begin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.state.Active || t.state.Running || (!t.state.Enabled && !t.manual) {
		return false
	}
	if t.state.NextRun == nil || time.Now().Before(*t.state.NextRun) {
		return false
	}
	t.manual = false
	t.state.Pending = false
	t.state.Running = true
	t.state.NextRun = nil
	now := time.Now().UTC()
	t.state.LastStarted = &now
	return true
}
func (t *Task) Finish(failed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state.Running = false
	t.state.Runs++
	now := time.Now().UTC()
	t.state.LastFinished = &now
	if t.report != nil {
		t.state.Results = t.report.Snapshot()
		t.state.History = append([]Round{{FinishedAt: now, Results: t.state.Results, Failed: failed}}, t.state.History...)
		if len(t.state.History) > 10 {
			t.state.History = t.state.History[:10]
		}
		t.report = nil
	}
	t.state.LastError = ""
	if failed {
		t.state.LastError = "部分同步失败，请查看连接与资源的抓取状态"
	}
	if t.state.Active && t.state.Enabled {
		t.arm(time.Duration(t.state.IntervalSeconds) * time.Second)
	}
}

// Run executes one shared refresh round at a time and joins on app cancellation.
func (t *Task) Run(ctx context.Context, poll func(context.Context) bool) error {
	t.Start()
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.Events():
			if t.Begin() {
				reportCtx, report := model.WithRefreshReport(ctx)
				t.mu.Lock()
				t.report = report
				t.mu.Unlock()
				t.Finish(poll(reportCtx))
			}
		}
	}
}
