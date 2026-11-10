package model

import (
	"context"
	"encoding/json"
	"time"
)

// AcademicQuery is owner-scoped by the repository argument, never supplied by models.
// OfferingID is the stable internal opening/teaching-class ID, CourseRef the opaque native compound key.
type AcademicQuery struct {
	Source          string     `json:"source,omitempty"`
	ConnectionID    string     `json:"connection_id,omitempty"`
	OfferingID      string     `json:"offering_id,omitempty"`
	CourseRef       string     `json:"course_ref,omitempty"`
	Kind            string     `json:"kind,omitempty"`
	Status          string     `json:"status,omitempty"`
	Search          string     `json:"search,omitempty"`
	PublishedAfter  *time.Time `json:"published_after,omitempty"`
	PublishedBefore *time.Time `json:"published_before,omitempty"`
	DeadlineAfter   *time.Time `json:"deadline_after,omitempty"`
	DeadlineBefore  *time.Time `json:"deadline_before,omitempty"`
	Limit           int        `json:"limit"`
	Offset          int        `json:"offset"`
}

type AcademicTime struct {
	Date        string     `json:"date,omitempty"` // date-only evidence, never midnight
	Value       *time.Time `json:"value,omitempty"`
	Raw         string     `json:"raw,omitempty"`
	Precision   string     `json:"precision"` // instant, local_minute, month_day_minute, day, unknown
	SourceField string     `json:"source_field,omitempty"`
	State       string     `json:"state"` // known, missing, not_collected, not_applicable, partial
}

type AcademicOffering struct {
	ID             string          `json:"offering_id"`
	CourseID       string          `json:"course_id"` // internal catalog course ID
	Source         string          `json:"source"`
	ConnectionID   string          `json:"connection_id"`
	CourseRef      string          `json:"course_ref"`
	NativeCourseID string          `json:"native_course_id"`
	NativeClassID  string          `json:"native_class_id,omitempty"`
	NativeTermID   string          `json:"native_term_id,omitempty"`
	CourseType     string          `json:"course_type,omitempty"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Details        json.RawMessage `json:"details"`
	Teacher        string          `json:"teacher,omitempty"`
	School         string          `json:"school,omitempty"`
	SourceTerms    json.RawMessage `json:"source_terms"`
	Coverage       json.RawMessage `json:"coverage"`
	RecordID       string          `json:"record_id"`
	ObservedAt     time.Time       `json:"observed_at"`
}

type AcademicItem struct {
	ID             string          `json:"item_id"`
	Source         string          `json:"source"`
	ConnectionID   string          `json:"connection_id"`
	OfferingID     string          `json:"offering_id,omitempty"`
	CourseRef      string          `json:"course_ref,omitempty"`
	CourseName     string          `json:"course_name,omitempty"`
	Kind           string          `json:"kind"`
	TaskType       string          `json:"task_type,omitempty"`
	Title          string          `json:"title"`
	Text           string          `json:"text,omitempty"`
	Author         string          `json:"author,omitempty"`
	NativeID       string          `json:"native_id"`
	NativeCourseID string          `json:"native_course_id,omitempty"`
	NativeClassID  string          `json:"native_class_id,omitempty"`
	NativeTermID   string          `json:"native_term_id,omitempty"`
	SourceTermID   string          `json:"source_term_id,omitempty"`
	ChapterID      string          `json:"chapter_id,omitempty"`
	LessonID       string          `json:"lesson_id,omitempty"`
	UnitID         string          `json:"unit_id,omitempty"`
	ContentID      string          `json:"content_id,omitempty"`
	Path           []string        `json:"path"`
	Published      AcademicTime    `json:"published"`
	Updated        AcademicTime    `json:"updated"`
	Start          AcademicTime    `json:"start"`
	End            AcademicTime    `json:"end"`
	Deadline       AcademicTime    `json:"deadline"`
	Status         string          `json:"status"` // normalized only when source evidence supports interpretation
	SourceStatus   string          `json:"source_status,omitempty"`
	PersonalState  json.RawMessage `json:"personal_state"`
	Metadata       json.RawMessage `json:"metadata"`
	Missing        json.RawMessage `json:"missing"`
	RecordIDs      []string        `json:"record_ids"`
	ObservedAt     time.Time       `json:"observed_at"`
}

type AcademicTree struct {
	Source       string             `json:"source"`
	ConnectionID string             `json:"connection_id"`
	Offerings    []AcademicOffering `json:"offerings"`
	Items        []AcademicItem     `json:"items"`
	Coverage     json.RawMessage    `json:"coverage"`
}

type AcademicRebuildResult struct {
	Source       string `json:"source,omitempty"`
	Connections  int    `json:"connections"`
	Offerings    int    `json:"offerings"`
	Items        int    `json:"items"`
	Records      int    `json:"records"`
	Unassociated int    `json:"unassociated"`
}

type AcademicRepository interface {
	Offerings(context.Context, string, AcademicQuery) ([]AcademicOffering, error)
	Items(context.Context, string, AcademicQuery) ([]AcademicItem, error)
	Tree(context.Context, string, string, string) (AcademicTree, error)
	Rebuild(context.Context, string, string) (AcademicRebuildResult, error)
}
