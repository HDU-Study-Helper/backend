package model

import (
	"context"
	"sort"
	"sync"
)

// RefreshResult contains counts only, never credentials, record IDs, or content.
type RefreshResult struct {
	Source      string `json:"source"`
	Status      string `json:"status"`
	Connections int    `json:"connections"`
	Attempts    int    `json:"attempts"`
	Succeeded   int    `json:"succeeded"`
	Failed      int    `json:"failed"`
	Created     int    `json:"created"`
	Updated     int    `json:"updated"`
	Unchanged   int    `json:"unchanged"`
	LastError   string `json:"last_error,omitempty"`
}
type RefreshReport struct {
	mu    sync.Mutex
	items map[string]RefreshResult
}
type refreshKey struct{}

func WithRefreshReport(ctx context.Context) (context.Context, *RefreshReport) {
	r := &RefreshReport{items: map[string]RefreshResult{}}
	return context.WithValue(ctx, refreshKey{}, r), r
}
func RefreshTracking(ctx context.Context) bool {
	r, _ := ctx.Value(refreshKey{}).(*RefreshReport)
	return r != nil
}
func updateRefresh(ctx context.Context, source string, fn func(*RefreshResult)) {
	r, _ := ctx.Value(refreshKey{}).(*RefreshReport)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.items[source]
	item.Source = source
	fn(&item)
	r.items[source] = item
}
func InitRefreshSource(ctx context.Context, source string, configured bool) {
	updateRefresh(ctx, source, func(v *RefreshResult) {
		v.Status = "no_connections"
		if !configured {
			v.Status = "not_configured"
		}
	})
}
func RefreshConnection(ctx context.Context, source string) {
	updateRefresh(ctx, source, func(v *RefreshResult) { v.Connections++; v.Status = "no_resources" })
}
func RefreshAttempt(ctx context.Context, source, code string) {
	updateRefresh(ctx, source, func(v *RefreshResult) {
		v.Attempts++
		if code != "" {
			v.Failed++
			v.LastError = code
		} else {
			v.Succeeded++
		}
	})
}
func RefreshFailure(ctx context.Context, source, code string) {
	updateRefresh(ctx, source, func(v *RefreshResult) { v.Failed++; v.LastError = code })
}

// RefreshCommitted is called only after successful transaction commit.
func RefreshCommitted(ctx context.Context, source string, created, updated, unchanged int) {
	updateRefresh(ctx, source, func(v *RefreshResult) { v.Created += created; v.Updated += updated; v.Unchanged += unchanged })
}
func (r *RefreshReport) Snapshot() []RefreshResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RefreshResult, 0, len(r.items))
	for _, v := range r.items {
		switch {
		case v.Failed > 0:
			if v.Succeeded > 0 {
				v.Status = "partial_failure"
			} else {
				v.Status = "failed"
			}
		case v.Attempts > 0:
			if v.Created+v.Updated > 0 {
				v.Status = "changed"
			} else {
				v.Status = "unchanged"
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

func RefreshSnapshot(ctx context.Context) []RefreshResult {
	r, _ := ctx.Value(refreshKey{}).(*RefreshReport)
	if r == nil {
		return []RefreshResult{}
	}
	return r.Snapshot()
}
