package model

import (
	"context"
	"sync"
	"testing"
)

func TestRefreshCountsAndFailureClassification(t *testing.T) {
	ctx, report := WithRefreshReport(context.Background())
	InitRefreshSource(ctx, "dws", true)
	InitRefreshSource(ctx, "chaoxing", true)
	InitRefreshSource(ctx, "mooc", false)
	RefreshConnection(ctx, "dws")
	RefreshCommitted(ctx, "dws", 2, 1, 4)
	RefreshAttempt(ctx, "dws", "")
	RefreshConnection(ctx, "chaoxing")
	RefreshAttempt(ctx, "chaoxing", "")
	RefreshAttempt(ctx, "chaoxing", "auth_required")
	bySource := map[string]RefreshResult{}
	for _, item := range report.Snapshot() {
		bySource[item.Source] = item
	}
	if r := bySource["dws"]; r.Status != "changed" || r.Created != 2 || r.Updated != 1 || r.Unchanged != 4 {
		t.Fatalf("counts: %+v", r)
	}
	if r := bySource["chaoxing"]; r.Status != "partial_failure" || r.Failed != 1 || r.Attempts != 2 {
		t.Fatalf("failure: %+v", r)
	}
	if bySource["mooc"].Status != "not_configured" {
		t.Fatal("not configured conflated with unchanged")
	}
	ctx, report = WithRefreshReport(context.Background())
	InitRefreshSource(ctx, "dws", true)
	if report.Snapshot()[0].Status != "no_connections" {
		t.Fatal("empty targets treated as unchanged")
	}
	RefreshConnection(ctx, "dws")
	RefreshAttempt(ctx, "dws", "")
	if report.Snapshot()[0].Status != "unchanged" {
		t.Fatal("successful empty page not unchanged")
	}
}
func TestConcurrentRefreshReportsAreIsolated(t *testing.T) {
	ctx, report := WithRefreshReport(context.Background())
	_, other := WithRefreshReport(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); RefreshCommitted(ctx, "dws", 1, 0, 0); _ = report.Snapshot() }()
	}
	wg.Wait()
	if report.Snapshot()[0].Created != 20 || len(other.Snapshot()) != 0 {
		t.Fatal("report counts leaked between rounds")
	}
	RefreshCommitted(context.Background(), "dws", 100, 0, 0)
	if report.Snapshot()[0].Created != 20 {
		t.Fatal("non-refresh operation changed report")
	}
}
