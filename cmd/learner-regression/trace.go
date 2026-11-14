package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agentadmin"
)

var runIDPattern = regexp.MustCompile(`Trusted run_id=([a-f0-9]+);`)

type recordingResolver struct {
	inner *agentadmin.SettingsManager
	dir   string
	mu    sync.Mutex
	calls map[string]int
}

func (r *recordingResolver) ResolveProvider(name string) f.ModelProvider {
	p := r.inner.ResolveProvider(name)
	if p == nil {
		return nil
	}
	return recordingProvider{p, r}
}
func (r *recordingResolver) ResolveModel(c f.ModelConfig) (f.ModelProvider, f.ModelConfig, error) {
	p, c, err := r.inner.ResolveModel(c)
	if err != nil {
		return nil, c, err
	}
	return recordingProvider{p, r}, c, nil
}

type recordingProvider struct {
	inner f.ModelProvider
	r     *recordingResolver
}

func (p recordingProvider) Complete(ctx context.Context, q f.ModelRequest) (f.ModelResponse, error) {
	run := "unknown"
	if len(q.Messages) > 0 {
		if match := runIDPattern.FindStringSubmatch(q.Messages[0].Content); len(match) == 2 {
			run = match[1]
		}
	}
	p.r.mu.Lock()
	if p.r.calls == nil {
		p.r.calls = map[string]int{}
	}
	p.r.calls[run]++
	n := p.r.calls[run]
	p.r.mu.Unlock()
	raw, err := json.Marshal(q)
	if err != nil {
		return f.ModelResponse{}, err
	}
	if err = os.WriteFile(filepath.Join(p.r.dir, fmt.Sprintf("%s-%03d-request.private.json", run, n)), raw, 0600); err != nil {
		return f.ModelResponse{}, err
	}
	toolBytes := 0
	for _, m := range q.Messages {
		if m.Role == "tool" {
			b, _ := json.Marshal(m)
			toolBytes += len(b)
		}
	}
	start := time.Now()
	out, callErr := p.inner.Complete(ctx, q)
	// Response bodies stay in private artifacts; public summaries contain only counts.
	if err = save(filepath.Join(p.r.dir, fmt.Sprintf("%s-%03d-response.private.json", run, n)), map[string]any{"response": out, "error": f.SafeError(callErr)}); err != nil {
		return f.ModelResponse{}, err
	}
	event := map[string]any{"run_id": run, "call": n, "request_bytes": len(raw), "messages": len(q.Messages), "tool_history_bytes": toolBytes, "duration_ms": time.Since(start).Milliseconds(), "usage": out.Usage, "error": f.SafeError(callErr), "at": time.Now().UTC()}
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	file, err := os.OpenFile(filepath.Join(p.r.dir, "requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return f.ModelResponse{}, err
	}
	encodeErr := json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if encodeErr != nil {
		return f.ModelResponse{}, encodeErr
	}
	if closeErr != nil {
		return f.ModelResponse{}, closeErr
	}
	return out, callErr
}
