// Package learnerstage defines the host-owned Learner editing transaction.
// It has no model, HTTP, platform or SQL dependencies.
package learnerstage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	f "academic/agent/framework"
)

const Protocol = "learner_staging_v2"

type Snapshot struct {
	Workflow  f.Workflow         `json:"workflow"`
	Memories  []f.Memory         `json:"memories"`
	Relations []f.MemoryRelation `json:"relations"`
}

type Commit struct {
	SessionID    string    `json:"session_id"`
	Scope        f.Scope   `json:"scope"`
	Path         string    `json:"path"`
	Operation    string    `json:"operation"`
	Revision     int       `json:"staging_revision"`
	BaselineHash string    `json:"baseline_hash"`
	Base         *Snapshot `json:"base,omitempty"`
	Desired      Snapshot  `json:"desired"`
}

type Receipt struct {
	SessionID       string            `json:"session_id"`
	Revision        int               `json:"staging_revision"`
	Operation       string            `json:"operation"`
	Status          string            `json:"status"`
	WorkflowID      string            `json:"workflow_id"`
	WorkflowVersion int               `json:"workflow_version"`
	MemoryIDs       []string          `json:"memory_ids"`
	IDMap           map[string]string `json:"id_map"`
	Verified        bool              `json:"verified"`
}

// Store is an optional host repository capability. Commit must atomically compare
// baseline, apply all changes and persist its idempotent receipt. No SQL occurs
// while the model is running. Snapshot includes all target PRI states/validities.
type Store interface {
	LearnerSnapshot(context.Context, f.Scope, string) (Snapshot, error)
	CommitLearnerStage(context.Context, Commit) (Receipt, error)
	LearnerStageReceipt(context.Context, string, string) (Receipt, error)
}

func Hash(s Snapshot) string {
	// Sort copies so repository ordering never changes optimistic identity.
	s.Memories = append([]f.Memory(nil), s.Memories...)
	s.Relations = append([]f.MemoryRelation(nil), s.Relations...)
	sort.Slice(s.Memories, func(i, j int) bool { return s.Memories[i].ID < s.Memories[j].ID })
	sort.Slice(s.Relations, func(i, j int) bool { return s.Relations[i].ID < s.Relations[j].ID })
	raw, _ := json.Marshal(s)
	// JSONB normalization is performed by dbbridge on reads; numbers remain
	// exact by hashing the serialized domain values directly.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func Clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
