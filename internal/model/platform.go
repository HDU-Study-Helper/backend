package model

import (
	"context"
	"encoding/json"
	"time"
)

type PlatformConnection struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Source    string    `json:"source"`
	State     string    `json:"state"`
	Enabled   bool      `json:"enabled"`
	ErrorCode string    `json:"error_code,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
type PlatformLease struct {
	Scope         DWSScope
	Token         int64
	CheckpointKey string // optional course partition; never used as an authorization capability
}
type PlatformState struct {
	Connection    PlatformConnection
	Session       []byte
	Cursor        []byte
	AuthExpiresAt time.Time
}
type PlatformRecord struct {
	ID         int64           `json:"id"`
	Source     string          `json:"source"`
	Kind       string          `json:"kind"`
	ExternalID string          `json:"external_id"`
	ParentID   string          `json:"parent_id,omitempty"`
	Hash       string          `json:"content_hash"`
	Content    json.RawMessage `json:"content"`
	ObservedAt time.Time       `json:"observed_at"`
	// CourseDelivery is a derived presentation rule, never source-confirmed data.
	CourseDelivery *CourseDelivery `json:"course_delivery,omitempty"`
}

// PlatformRepository is the central storage port; adapters never receive SQL access.
type PlatformRepository interface {
	Create(context.Context, string, string) (PlatformConnection, error)
	List(context.Context, string) ([]PlatformConnection, error)
	Get(context.Context, DWSScope) (PlatformConnection, error)
	Targets(context.Context, string, int) ([]PlatformConnection, error)
	Acquire(context.Context, DWSScope) (PlatformLease, error)
	Release(context.Context, PlatformLease) error
	Load(context.Context, PlatformLease, string) (PlatformState, error)
	SaveAuth(context.Context, PlatformLease, []byte, string, time.Time) error
	Commit(context.Context, PlatformLease, string, []PlatformRecord, []byte, []byte) error
	Fail(context.Context, PlatformLease, string) error
	Disconnect(context.Context, DWSScope) error
	Records(context.Context, DWSScope, string, int64, int) ([]PlatformRecord, error)
}
