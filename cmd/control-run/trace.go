package main

import (
	f "academic/agent/framework"
	"academic/backend/internal/agentadmin"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var trustedRun = regexp.MustCompile(`Trusted run_id=([a-f0-9]{1,128});`)

type traceResolver struct {
	inner *agentadmin.SettingsManager
	dir   string
	mu    sync.Mutex
	calls map[string]int
}

func (r *traceResolver) ResolveProvider(name string) f.ModelProvider {
	p := r.inner.ResolveProvider(name)
	if p == nil {
		return nil
	}
	return traceProvider{p, r}
}
func (r *traceResolver) ResolveModel(c f.ModelConfig) (f.ModelProvider, f.ModelConfig, error) {
	p, c, e := r.inner.ResolveModel(c)
	if e != nil {
		return nil, c, e
	}
	return traceProvider{p, r}, c, nil
}

type traceProvider struct {
	inner    f.ModelProvider
	recorder *traceResolver
}

func (p traceProvider) Complete(ctx context.Context, q f.ModelRequest) (f.ModelResponse, error) {
	run := ""
	if len(q.Messages) > 0 {
		if m := trustedRun.FindStringSubmatch(q.Messages[0].Content); len(m) == 2 {
			run = m[1]
		}
	}
	if run == "" {
		return f.ModelResponse{}, f.Fail("INTERNAL_ERROR", "model trace requires a trusted run identity")
	}
	r := p.recorder
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[run]++
	n := r.calls[run]
	r.mu.Unlock()
	if e := save(filepath.Join(r.dir, fmt.Sprintf("%s-%04d-request.private.json", run, n)), q); e != nil {
		return f.ModelResponse{}, e
	}
	start := time.Now()
	out, callErr := p.inner.Complete(ctx, q)
	if e := save(filepath.Join(r.dir, fmt.Sprintf("%s-%04d-response.private.json", run, n)), map[string]any{"response": out, "error": f.SafeError(callErr)}); e != nil {
		return f.ModelResponse{}, e
	}
	event := map[string]any{"run_id": run, "call": n, "duration_ms": time.Since(start).Milliseconds(), "usage": out.Usage, "error": f.SafeError(callErr), "at": time.Now().UTC()}
	r.mu.Lock()
	defer r.mu.Unlock()
	file, e := os.OpenFile(filepath.Join(r.dir, "requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return f.ModelResponse{}, e
	}
	e = json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if e != nil {
		return f.ModelResponse{}, e
	}
	if closeErr != nil {
		return f.ModelResponse{}, closeErr
	}
	return out, callErr
}
