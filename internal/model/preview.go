package model

import (
	"context"
	"time"
)

type ResourcePreview struct {
	Kind           string     `json:"kind"`
	RecordCount    int64      `json:"record_count"`
	EntityCount    int64      `json:"entity_count"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastErrorCode  string     `json:"last_error_code,omitempty"`
	LastDurationMS *int64     `json:"last_duration_ms,omitempty"`
	LastPageCount  *int       `json:"last_page_count,omitempty"`
	InProgress     bool       `json:"in_progress"`
	HasMore        *bool      `json:"has_more,omitempty"`
}
type ConnectionPreview struct {
	Source       string            `json:"source"`
	ConnectionID string            `json:"connection_id"`
	State        string            `json:"state"`
	Enabled      bool              `json:"enabled"`
	ErrorCode    string            `json:"error_code,omitempty"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Resources    []ResourcePreview `json:"resources"`
}
type PreviewRepository interface {
	Overview(context.Context, string) ([]ConnectionPreview, error)
	Records(context.Context, string, string, string, string, int64, int) ([]PlatformRecord, error)
}

// SyncObserver is optional instrumentation. Fencing is checked for each write.
type SyncObserver interface {
	StartSync(context.Context, PlatformLease, string) error
	FinishSync(context.Context, PlatformLease, string, string, int64, int, bool) error
}
