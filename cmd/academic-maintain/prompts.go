package main

import (
	f "academic/agent/framework"
	"academic/backend/internal/dbbridge"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// syncPrompts updates definitions without executing Agents or changing models.
// Trigger context capacity accommodates complete course batches.
func syncPrompts(ctx context.Context, db *dbbridge.DB, owner string) error {
	agents, err := db.Agents().ListAgents(ctx, owner, 100, "")
	if err != nil {
		return err
	}
	replaced := 0
	for _, a := range agents {
		switch a.Type {
		case "trigger", "filter", "learner", "worker":
		default:
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../agent", a.Type+"-agent", "system-prompt.md"))
		if err != nil {
			return err
		}
		prompt := string(raw)
		for _, target := range agents {
			if target.Type == "worker" {
				prompt = strings.ReplaceAll(prompt, "{{WORKER_AGENT_ID}}", target.ID)
			}
			if target.Type == "learner" {
				prompt = strings.ReplaceAll(prompt, "{{LEARNER_AGENT_ID}}", target.ID)
			}
		}
		if strings.Contains(prompt, "{{") {
			return fmt.Errorf("unresolved prompt placeholder")
		}
		a.SystemPrompt = prompt
		if a.Type == "trigger" && a.Limits.ContextBytes < 1<<20 {
			a.Limits.ContextBytes = 1 << 20
		}
		if _, err = (f.Registry{Repository: db.Agents()}).Save(ctx, owner, a); err != nil {
			return err
		}
		replaced++
	}
	fmt.Printf("Updated %d Agent prompts; no Agent execution started\n", replaced)
	return nil
}
