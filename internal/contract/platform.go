// Package contract contains infrastructure-independent platform ports.
package contract

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrUnsupported          = errors.New("unsupported")
	ErrAuthRequired         = errors.New("auth_required")
	ErrParse                = errors.New("parse_failed")
	ErrRateLimited          = errors.New("rate_limited")
	ErrVerificationRequired = errors.New("verification_required")
	ErrUpstream             = errors.New("upstream_unavailable")
	ErrInvalid              = errors.New("invalid_argument")
)

type Scope struct {
	UserID       string
	ConnectionID string
}
type Resource string

const (
	Course              Resource = "course"
	Chapter             Resource = "chapter"
	Lesson              Resource = "lesson"
	Schedule            Resource = "schedule"
	Assignment          Resource = "assignment"
	Exam                Resource = "exam"
	Progress            Resource = "progress"
	Notice              Resource = "notice"
	Discussion          Resource = "discussion"
	Material            Resource = "material"
	DiscussionReplyUser Resource = "discussion_reply_user"
)

type Descriptor struct {
	Source         string     `json:"source"`
	Name           string     `json:"name"`
	QRLogin        bool       `json:"qr_login"`
	PasswordLogin  bool       `json:"password_login,omitempty"`
	ManualSyncOnly bool       `json:"manual_sync_only,omitempty"`
	Resources      []Resource `json:"resources"`
	Limitations    []string   `json:"limitations"`
}

// Session is opaque sensitive adapter state. Only the credential layer persists it.
type Session []byte

// AuthResult never exposes Session or upstream state to HTTP callers.
type AuthResult struct {
	Status      string        `json:"status"`           // pending/scanned/authorized/expired
	QRURL       string        `json:"qr_url,omitempty"` // HTTPS official QR image or scan URI; never a token API URL.
	QRImage     []byte        `json:"-"`                // PNG/JPEG bytes when the platform provides an image.
	QRMediaType string        `json:"-"`
	ExpiresAt   time.Time     `json:"expires_at"`
	PollAfter   time.Duration `json:"-"`
	Session     Session       `json:"-"`
}

// Record is a source observation, not a confirmed academic fact.
type Record struct {
	Kind          Resource       `json:"kind"`
	ExternalID    string         `json:"external_id"`
	ParentID      string         `json:"parent_id,omitempty"`
	Title         string         `json:"title,omitempty"`
	Content       string         `json:"content,omitempty"`
	Fields        map[string]any `json:"fields"`
	SourceVersion string         `json:"source_version,omitempty"`
	ObservedAt    time.Time      `json:"observed_at"`
}
type Page struct {
	Records    []Record
	NextCursor string // sensitive opaque state, encrypted by the host.
	HasMore    bool
	Session    Session // return updated cookies/state; nil means retain input.
}

// Each call restores a private cookie jar from Session. Adapters never share global sessions,
// start loops, write SQL or register HTTP routes. Host scope is trusted and never model-controlled.
type Adapter interface {
	Describe() Descriptor
	BeginAuth(context.Context, Scope) (AuthResult, error)
	PollAuth(context.Context, Scope, Session) (AuthResult, error)
	Sync(context.Context, Scope, Session, Resource, string) (Page, error)
}

// Optional lifecycle ports for platforms whose official login requires a live connection.
type AuthCloser interface{ Close(context.Context) error }
type AuthCanceller interface {
	CancelAuth(context.Context, Scope) error
}
type Dependencies struct{ HTTPClient *http.Client }
