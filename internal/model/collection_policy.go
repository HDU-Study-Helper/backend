package model

import (
	"context"
	"time"
)

// CollectionExclusion identifies an existing owner-scoped teaching class or term.
type CollectionExclusion struct {
	OfferingID    string `json:"offering_id"`
	Source        string `json:"source"`
	ConnectionID  string `json:"connection_id"`
	CourseRef     string `json:"course_ref"`
	Name          string `json:"name"`
	NativeClassID string `json:"native_class_id,omitempty"`
	NativeTermID  string `json:"native_term_id,omitempty"`
}
type CollectionPolicy struct {
	Version   int                   `json:"version"`
	Excluded  []CollectionExclusion `json:"excluded"`
	UpdatedAt *time.Time            `json:"updated_at,omitempty"`
}
type CollectionPolicyRepository interface {
	CollectionPolicy(context.Context, string) (CollectionPolicy, error)
	SaveCollectionPolicy(context.Context, string, int, []string) (CollectionPolicy, error)
	ExcludedCourseRefs(context.Context, DWSScope) ([]string, error)
}
