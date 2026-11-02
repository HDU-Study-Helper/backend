package model

import "context"

// CoursePreview groups latest source records by the platform's course+class identity.
type CoursePreview struct {
	Key         string            `json:"key"`
	CourseID    string            `json:"course_id"`
	ClassID     string            `json:"class_id"`
	Title       string            `json:"title"`
	RecordCount int64             `json:"record_count"`
	EntityCount int64             `json:"entity_count"`
	Resources   []ResourcePreview `json:"resources"`
}
type CoursePreviewRepository interface {
	Courses(context.Context, string, string, string) ([]CoursePreview, int64, error)
	CourseRecords(context.Context, string, string, string, string, string, int64, int) ([]PlatformRecord, error)
}
