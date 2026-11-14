package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

type caseRunner func(context.Context, int, course) caseResult

// Only the coordinator writes cumulative files. Workers return results; this
// preserves original manifest order even when completion order differs.
func runCases(ctx context.Context, parallel int, dir, attemptDir string, m manifest, prior []caseResult, run caseRunner) error {
	if parallel < 1 || parallel > 4 {
		return errors.New("parallel must be between 1 and 4")
	}
	latest := map[string]caseResult{}
	for _, entry := range prior {
		latest[entry.OfferingID] = entry
	}
	attemptResults := map[string]caseResult{}
	persist := func() error {
		results := orderedResults(m.Courses, latest)
		if err := save(filepath.Join(attemptDir, "results.private.json"), orderedResults(m.Courses, attemptResults)); err != nil {
			return err
		}
		if err := save(filepath.Join(dir, "results.private.json"), results); err != nil {
			return err
		}
		verified := 0
		review := 0
		for _, entry := range results {
			if entry.Verified {
				verified++
			}
			if entry.Disposition == "review_required" {
				review++
			}
		}
		return save(filepath.Join(dir, "progress.json"), map[string]any{"total": len(m.Courses), "finished": len(results), "verified": verified, "failed": len(results) - verified, "review_required": review, "schema": m.TargetSchema, "parallel": parallel, "cancelled": ctx.Err() != nil, "updated_at": time.Now().UTC()})
	}
	if err := persist(); err != nil {
		return err
	}
	type job struct {
		index int
		c     course
	}
	pending := []job{}
	for i, c := range m.Courses {
		if prior, ok := latest[c.ID]; !ok || !prior.Verified {
			pending = append(pending, job{i, c})
		}
	}
	jobs := make(chan job)
	completed := make(chan caseResult, parallel)
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if workerCtx.Err() != nil {
					return
				}
				entry := run(workerCtx, j.index, j.c)
				completed <- entry
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, j := range pending {
			select {
			case jobs <- j:
			case <-workerCtx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(completed) }()
	var persistenceErr error
	for entry := range completed {
		latest[entry.OfferingID] = entry
		attemptResults[entry.OfferingID] = entry
		if persistenceErr == nil {
			if err := persist(); err != nil {
				persistenceErr = err
				cancel()
			}
		}
		fmt.Printf("Course result: verified=%t disposition=%s\n", entry.Verified, entry.Disposition)
	}
	if persistenceErr != nil {
		return persistenceErr
	}
	if err := persist(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	failed := 0
	for _, c := range m.Courses {
		if result, ok := latest[c.ID]; !ok || !result.Verified {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("regression finished with %d unverified/failed cases; retained attempts require inspection", failed)
	}
	return nil
}

func orderedResults(courses []course, results map[string]caseResult) []caseResult {
	ordered := []caseResult{}
	for _, c := range courses {
		if entry, ok := results[c.ID]; ok {
			ordered = append(ordered, entry)
		}
	}
	return ordered
}
