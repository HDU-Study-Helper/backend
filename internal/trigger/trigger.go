// Package trigger defines the in-process port for future Agent reactions to business changes.
// It does not install PostgreSQL triggers, run a worker or call a model.
package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"
)

type Operation string

const (
	Created Operation = "created"
	Updated Operation = "updated"
	Deleted Operation = "deleted"
)

// Change is emitted after a successful transaction. It contains safe business
// references and field names, never credential state or historical content.
type Change struct {
	UserID        string    `json:"user_id"`
	ConnectionID  string    `json:"connection_id,omitempty"`
	Source        string    `json:"source"`
	Resource      string    `json:"resource"`
	RecordID      string    `json:"record_id"`
	ExternalID    string    `json:"external_id,omitempty"`
	ParentID      string    `json:"parent_id,omitempty"`
	Operation     Operation `json:"operation"`
	ChangedFields []string  `json:"changed_fields"`
	OccurredAt    time.Time `json:"occurred_at"`
}

// Handler is injected by application composition. Future Agent code can implement
// it without SQL, Cookie access or a dependency from trigger to Agent.
// Handle must return promptly. Delivery is best effort; this port is not a queue.
type Handler interface {
	Handle(context.Context, Change) error
}
type HandlerFunc func(context.Context, Change) error

func (f HandlerFunc) Handle(ctx context.Context, c Change) error { return f(ctx, c) }

type Noop struct{}

func (Noop) Handle(context.Context, Change) error { return nil }

// Eligible excludes DWS messages until their weighting policy is defined.
// Infrastructure tables (sessions, credentials, checkpoints, leases, jobs and
// scrape status) are never published by the storage integration.
func Eligible(c Change) bool {
	if c.UserID == "" || c.RecordID == "" {
		return false
	}
	if c.Operation != Created && c.Operation != Updated && c.Operation != Deleted {
		return false
	}
	if c.Source == "dws" && c.Resource == "message" {
		return false
	}
	switch c.Resource {
	case "course", "schedule", "assignment", "exam", "progress", "notice", "discussion", "discussion_reply_user", "material", "attachment":
		return true
	default:
		return false
	}
}

// FieldChanges returns stable top-level business keys whose JSON values changed.
// observed_at is collection metadata, not a business mutation.
func FieldChanges(before, after json.RawMessage) []string {
	var old, new map[string]json.RawMessage
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &new)
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range new {
		keys[k] = true
	}
	out := []string{}
	for k := range keys {
		if k == "observed_at" {
			continue
		}
		if !bytes.Equal(canonical(old[k]), canonical(new[k])) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
func canonical(raw json.RawMessage) []byte {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return raw
	}
	out, _ := json.Marshal(value)
	return out
}
