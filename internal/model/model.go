// Package model defines storage contracts shared by application services.
package model

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound        = errors.New("not_found")
	ErrConflict        = errors.New("conflict")
	ErrInvalidArgument = errors.New("invalid_argument")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrLastAdmin       = errors.New("last_admin")
	ErrLeaseLost       = errors.New("lease_lost")
	ErrUnsupported     = errors.New("unsupported")
	ErrInternal        = errors.New("storage_unavailable")
)

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session stores only a SHA-256 hash of a cryptographically random bearer token.
type Session struct {
	Hash      []byte    `json:"-"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	// ExpectedPasswordHash binds issuance to the password verified by the caller.
	// It is checked under the user row lock and is never stored in sessions.
	ExpectedPasswordHash string `json:"-"`
}
type UserUpdate struct {
	DisplayName  *string
	PasswordHash *string
	Role         *Role
	Disabled     *bool
}
type UserRepository interface {
	Create(context.Context, User) (User, error)
	GetByID(context.Context, string) (User, error)
	GetByUsername(context.Context, string) (User, error)
	List(context.Context, int, string) ([]User, error)
	Update(context.Context, string, UserUpdate) (User, error)
	Delete(context.Context, string) error
	BootstrapAdmin(context.Context, User) (User, bool, error)
	CreateSession(context.Context, Session) error
	GetSessionUser(context.Context, []byte) (User, error)
	DeleteSession(context.Context, []byte) error
	RevokeUserSessions(context.Context, string) error
}

type DWSMode string

const (
	DWSModePersonal DWSMode = "personal"
	DWSModeSchool   DWSMode = "school"
)

type DWSScope struct {
	UserID       string
	ConnectionID string
}
type DWSConnection struct {
	ID                  string    `json:"id"`
	UserID              string    `json:"user_id"`
	Profile             string    `json:"profile"`
	CorpID              string    `json:"corp_id,omitempty"`
	UpstreamUserID      string    `json:"upstream_user_id,omitempty"`
	Mode                DWSMode   `json:"mode"`
	Enabled             bool      `json:"enabled"`
	State               string    `json:"state"`
	WorkspaceCiphertext []byte    `json:"-"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// DWSIdentity is the exact identity verified by the official login and readonly status.
type DWSIdentity struct {
	Profile        string
	CorpID         string
	UpstreamUserID string
}

type DWSConnectionUpdate struct {
	Profile        *string
	CorpID         *string
	UpstreamUserID *string
	State          *string
}
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Lease is a fencing capability bound to exactly one owner/connection/group.
type Lease struct {
	Scope     DWSScope
	GroupID   string
	Token     int64
	ExpiresAt time.Time
}
type Checkpoint struct {
	GroupID            string    `json:"group_id"`
	CursorCiphertext   []byte    `json:"-"`
	CLITokenCiphertext []byte    `json:"-"`
	PollWatermark      time.Time `json:"poll_watermark"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Message stores the latest source state under a stable record ID. Hash is lowercase SHA-256 hex.
type Message struct {
	ID            int64     `json:"id"`
	GroupID       string    `json:"group_id"`
	ExternalID    string    `json:"external_id"`
	SourceVersion string    `json:"source_version"`
	ContentHash   string    `json:"content_hash"`
	Content       []byte    `json:"content"`
	ObservedAt    time.Time `json:"observed_at"`
}
type Attachment struct {
	ID        string `json:"id"`
	MessageID int64  `json:"message_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
	Data      []byte `json:"-"`
}
type SyncJob struct {
	ID        string    `json:"id"`
	GroupID   string    `json:"group_id"`
	State     string    `json:"state"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SyncCommit atomically persists observations and advances encrypted upstream state.
// Attachments reference the message's returned/stable ID; archive separately when needed.
type SyncCommit struct {
	Messages            []Message
	Checkpoint          Checkpoint
	WorkspaceCiphertext []byte
	JobID               string
}

const MaxAttachmentBytes = 8 << 20

// MaxWorkspaceBytes bounds ciphertext, including archive headers and AEAD overhead.
// Credential services separately limit plaintext contents to 16 MiB.
const MaxWorkspaceBytes = 21 << 20
const MaxMessageBytes = 1 << 20

type DWSRepository interface {
	CreateConnection(context.Context, DWSConnection) (DWSConnection, error)
	// CreatePendingConnection reuses an enabled, credential-free pending connection with no identity.
	CreatePendingConnection(context.Context, string) (DWSConnection, error)
	// CompleteLink verifies the lease and atomically binds identity, workspace and active state.
	CompleteLink(context.Context, Lease, DWSIdentity, []byte) error
	GetConnection(context.Context, DWSScope) (DWSConnection, error)
	ListConnections(context.Context, string) ([]DWSConnection, error)
	// ListPollTargets is a trusted worker-only scheduling port; never expose it over HTTP.
	ListPollTargets(context.Context, int, string) ([]DWSConnection, error)
	UpdateConnection(context.Context, DWSScope, DWSConnectionUpdate) (DWSConnection, error)
	Disconnect(context.Context, DWSScope) error
	DeleteConnection(context.Context, DWSScope) error
	LoadWorkspace(context.Context, DWSScope) ([]byte, error)
	SaveWorkspace(context.Context, Lease, []byte) error
	SetGroups(context.Context, DWSScope, []Group) error
	ListGroups(context.Context, DWSScope) ([]Group, error)
	AcquireLease(context.Context, DWSScope, string, time.Duration) (Lease, error)
	RenewLease(context.Context, Lease, time.Duration) (Lease, error)
	ReleaseLease(context.Context, Lease) error
	GetCheckpoint(context.Context, DWSScope, string) (Checkpoint, error)
	CommitSync(context.Context, Lease, SyncCommit) error
	ListMessages(context.Context, DWSScope, string, int64, int) ([]Message, error)
	PutAttachment(context.Context, Lease, Attachment) error
	GetAttachment(context.Context, DWSScope, int64, string) (Attachment, error)
	CreateJob(context.Context, DWSScope, string) (SyncJob, error)
	GetJob(context.Context, DWSScope, string) (SyncJob, error)
	ListPendingJobs(context.Context, DWSScope, int) ([]SyncJob, error)
	StartJob(context.Context, Lease, string) error
	// FailJob only fails queued work when enqueueing fails; workers use FailLeasedJob.
	FailJob(context.Context, DWSScope, string, string) error
	FailLeasedJob(context.Context, Lease, string, string) error
}
