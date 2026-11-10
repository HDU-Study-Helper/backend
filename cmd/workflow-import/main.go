// Command workflow-import installs an explicit owner-scoped definition through
// the existing Repository. It never invokes a model or executes the Workflow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/dbbridge"
)

func run() error {
	owner := flag.String("owner-id", "", "Verified destination owner ID")
	agent := flag.String("agent-id", "", "Existing enabled agent used for the repository scope")
	file := flag.String("file", "", "Workflow JSON file")
	flag.Parse()
	if *owner == "" || *agent == "" || *file == "" {
		return errors.New("owner-id, agent-id and file are required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return errors.New("cannot read workflow file")
	}
	var input struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Status      string          `json:"status"`
		Definition  json.RawMessage `json:"definition"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return errors.New("invalid workflow file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("cannot open configured database")
	}
	defer db.Close()
	repo := db.Agents()
	scope := f.Scope{OwnerID: *owner, AgentID: *agent}
	actor, err := repo.GetAgent(ctx, scope)
	if err != nil || actor.Status != "enabled" {
		return errors.New("scope agent must be enabled and owned by destination user")
	}
	// Re-running never silently overwrites a user's existing definition.
	after := ""
	for {
		items, e := repo.ListWorkflows(ctx, *owner, 100, after)
		if e != nil {
			return errors.New("cannot inspect destination workflows")
		}
		for _, w := range items {
			if w.Name == input.Name {
				return errors.New("workflow with this name already exists; refusing duplicate import")
			}
		}
		if len(items) < 100 {
			break
		}
		after = items[len(items)-1].ID
	}
	w, err := repo.SaveWorkflow(ctx, scope, f.Workflow{Name: input.Name, Description: input.Description, Status: input.Status, Definition: input.Definition}, true)
	if err != nil {
		return errors.New("workflow save failed")
	}
	read, err := repo.GetWorkflow(ctx, *owner, w.ID)
	if err != nil {
		return errors.New("workflow saved but readback failed")
	}
	var expected, actual any
	if json.Unmarshal(input.Definition, &expected) != nil || json.Unmarshal(read.Definition, &actual) != nil || !reflect.DeepEqual(expected, actual) || read.Name != input.Name || read.Description != input.Description || read.Status != input.Status || read.Version != 1 {
		return errors.New("workflow saved but verification failed")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"passed": true, "workflow_id": read.ID, "owner_user_id": read.OwnerID, "name": read.Name, "status": read.Status, "version": read.Version, "created_by_agent_id": read.CreatedBy, "verified": []string{"owner_scope", "definition_readback", "status_and_version"}, "workflow_executed": false})
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
