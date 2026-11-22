package model

import (
	"context"
	"time"
)

// FirstEvidenceScope versions the read policy so retries can revisit skips from
// runs executed before the ordinary tools were restored.
const FirstEvidenceScope = "owner_week_standard_tools"

type firstReadWindowKey struct{}
type firstReadWindow struct {
	owner string
	asOf  time.Time
}

// WithFirstReadWindow is host-only; tool JSON cannot supply a window.
func WithFirstReadWindow(ctx context.Context, owner string, asOf time.Time) context.Context {
	return context.WithValue(ctx, firstReadWindowKey{}, firstReadWindow{owner, asOf})
}
func FirstReadWindow(ctx context.Context, owner string) (since, asOf time.Time, ok bool) {
	w, ok := ctx.Value(firstReadWindowKey{}).(firstReadWindow)
	if !ok || w.owner != owner || w.asOf.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return w.asOf.Add(-7 * 24 * time.Hour), w.asOf, true
}

// FirstEvidence is a whitelisted source projection for cold-start research.
// Course identity is host assigned. An empty Entity means an unassociated observation.
type FirstEvidence struct {
	Ref    string `json:"ref"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Text   string `json:"text,omitempty"`
	Entity string `json:"entity,omitempty"`
	Course string `json:"course,omitempty"`
}
type FirstPacket struct {
	AsOf    time.Time       `json:"as_of"`
	Since   time.Time       `json:"since"`
	Courses []FirstEvidence `json:"courses"` // retained for reading old batches
	Scope   string          `json:"evidence_scope,omitempty"`
	Targets []FirstCourse   `json:"targets"`
	Missing map[string]int  `json:"missing_publication_time"`
}
type FirstCourse struct {
	Course  FirstEvidence   `json:"course"`
	Context []FirstEvidence `json:"context"`
}
type FirstStatus struct {
	State string `json:"state"`
	Batch string `json:"batch_id,omitempty"`
	Error string `json:"error,omitempty"`
}
type FirstRunRepository interface {
	FirstStatus(context.Context, string) (FirstStatus, error)
	StartFirst(context.Context, string) (FirstStatus, error)
	FirstInput(context.Context, string, string) (FirstPacket, error)
}
