package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	f "academic/agent/framework"
)

func resumeFixture(t *testing.T) (string, manifest) {
	t.Helper()
	dir := t.TempDir()
	m := manifest{Mode: "interrupted", TargetSchema: "learner_regression_20261114_010000_0123456789abcdef", Courses: []course{{Owner: "owner", Connection: "conn", Source: "chaoxing", ID: "one", Ref: "ref", Record: "1", Name: "Course"}}}
	m.CourseDigest = courseDigest(m.Courses)
	if err := save(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func TestResumeManifestRejectsTamperingAndProductionSchema(t *testing.T) {
	for _, kind := range []string{"production", "prefix only", "digest", "frozen copy", "duplicate course", "foreign result", "preparing"} {
		t.Run(kind, func(t *testing.T) {
			dir, m := resumeFixture(t)
			switch kind {
			case "production":
				m.TargetSchema = "public"
			case "prefix only":
				m.TargetSchema = "learner_regression_public"
			case "digest":
				m.Courses[0].Ref = "changed"
			case "frozen copy":
				if err := save(filepath.Join(dir, "courses.frozen.private.json"), []course{}); err != nil {
					t.Fatal(err)
				}
			case "duplicate course":
				m.Courses = append(m.Courses, m.Courses[0])
				m.CourseDigest = courseDigest(m.Courses)
			case "foreign result":
				if err := save(filepath.Join(dir, "results.private.json"), []caseResult{{OfferingID: "other"}}); err != nil {
					t.Fatal(err)
				}
			case "preparing":
				m.Mode = "preparing"
			}
			if err := save(filepath.Join(dir, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadResumeManifest(dir); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}

func TestResumeRequiresExplicitExecutionBeforeAnyConnection(t *testing.T) {
	err := resume(context.Background(), options{resumeDir: t.TempDir()})
	if err == nil {
		t.Fatal("resume implicitly executed")
	}
}

func TestAtomicArtifactsAndDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.json")
	if err := save(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := save(path, map[string]int{"b": 2}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]int
	if json.Unmarshal(raw, &data) != nil || data["b"] != 2 || len(data) != 1 {
		t.Fatal("partial or appended result")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe artifact permissions")
	}
	release, err := lockDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, e := lockDirectory(dir); e == nil {
		second()
		t.Fatal("duplicate executor accepted")
	}
	release()
	again, err := lockDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	again()
	link := filepath.Join(dir, "link.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err = save(link, map[string]int{"x": 3}); err == nil {
		t.Fatal("symlink overwrite allowed")
	}
}

func TestParallelCasesSkipVerifiedAndPreserveFrozenOrder(t *testing.T) {
	dir, m := resumeFixture(t)
	m.Courses = []course{{ID: "first"}, {ID: "second"}, {ID: "third"}, {ID: "fourth"}}
	attempt := filepath.Join(dir, "attempt")
	if err := os.Mkdir(attempt, 0700); err != nil {
		t.Fatal(err)
	}
	prior := []caseResult{{OfferingID: "first", RunID: "old-success", Verified: true}, {OfferingID: "third", RunID: "old-failure"}}
	var active, peak atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() {
		done <- runCases(context.Background(), 2, dir, attempt, m, prior, func(_ context.Context, index int, c course) caseResult {
			if c.ID == "first" {
				t.Error("verified case replayed")
			}
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			entered <- struct{}{}
			<-gate
			active.Add(-1)
			return caseResult{OfferingID: c.ID, RunID: "new-" + c.ID, Verified: true}
		})
	}()
	<-entered
	<-entered
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("parallel bound not exercised: %d", peak.Load())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "results.private.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results []caseResult
	if json.Unmarshal(raw, &results) != nil {
		t.Fatal("invalid results")
	}
	for i, r := range results {
		if r.OfferingID != m.Courses[i].ID {
			t.Fatal("completion order replaced manifest order")
		}
	}
	if results[0].RunID != "old-success" {
		t.Fatal("verified receipt overwritten")
	}
	raw, err = os.ReadFile(filepath.Join(attempt, "results.private.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fresh []caseResult
	if json.Unmarshal(raw, &fresh) != nil || len(fresh) != 3 {
		t.Fatal("attempt should contain only new cases")
	}
}

func TestCancellationPersistsReturnedAudit(t *testing.T) {
	dir, m := resumeFixture(t)
	attempt := filepath.Join(dir, "attempt")
	if err := os.Mkdir(attempt, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := runCases(ctx, 1, dir, attempt, m, nil, func(_ context.Context, _ int, c course) caseResult {
		cancel()
		return caseResult{OfferingID: c.ID, RunID: "cancelled", Record: &f.RunRecord{ID: "cancelled", Status: "failed"}, Error: f.SafeError(context.Canceled)}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	raw, e := os.ReadFile(filepath.Join(attempt, "results.private.json"))
	if e != nil {
		t.Fatal(e)
	}
	var results []caseResult
	if json.Unmarshal(raw, &results) != nil || len(results) != 1 || results[0].Record == nil {
		t.Fatal("cancellation discarded audit")
	}
}

type workflowScanRepo struct {
	f.Repository
	workflows []f.Workflow
	gets      int
}

func (r *workflowScanRepo) ListWorkflows(context.Context, string, int, string) ([]f.Workflow, error) {
	return r.workflows, nil
}
func (r *workflowScanRepo) GetWorkflow(_ context.Context, _ string, id string) (f.Workflow, error) {
	r.gets++
	for _, w := range r.workflows {
		if w.ID == id {
			return w, nil
		}
	}
	return f.Workflow{}, errors.New("missing")
}
func TestResumeDetectsExistingSameEntityBeforeModelReplay(t *testing.T) {
	c := course{Owner: "owner", Source: "chaoxing", Connection: "conn", Ref: "ref"}
	repo := &workflowScanRepo{workflows: []f.Workflow{{ID: "saved", Status: "draft", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"source=chaoxing|connection=conn|course_ref=ref"}}`)}}}
	id, err := matchingWorkflow(context.Background(), repo, c)
	if err != nil || id != "saved" || repo.gets != 1 {
		t.Fatal("existing write was not reconciled", id, err)
	}
}
