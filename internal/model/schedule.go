package model

import (
	"context"
	"time"
)

type Semester struct {
	AcademicYear int `json:"academic_year"`
	Term         int `json:"term"`
}

type ScheduleFields struct {
	TeachingWeek int       `json:"teaching_week"`
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	Title        string    `json:"title"`
	Details      string    `json:"details"`
}

type ScheduleChange struct {
	Mode          string         `json:"mode"`
	Before        ScheduleFields `json:"before"`
	After         ScheduleFields `json:"after"`
	EffectiveWeek int            `json:"effective_week"`
	OriginalID    string         `json:"original_id"`
	At            time.Time      `json:"at"`
}

type SchedulePatch struct {
	Fields []string       `json:"fields"`
	After  ScheduleFields `json:"after"`
}

type ScheduleMod struct {
	WeeksRaw      string           `json:"weeks_raw,omitempty"`
	TeachingWeeks []int            `json:"teaching_weeks,omitempty"`
	WeekPattern   string           `json:"week_pattern,omitempty"`
	Base          *ScheduleFields  `json:"base,omitempty"`
	Permanent     *SchedulePatch   `json:"permanent,omitempty"`
	Temporary     *SchedulePatch   `json:"temporary,omitempty"`
	Deleted       bool             `json:"deleted,omitempty"`
	History       []ScheduleChange `json:"history"`
}

type Schedule struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Semester
	ScheduleFields
	Source      string      `json:"source"`
	TimeStatus  string      `json:"time_status"`
	EventStatus string      `json:"event_status"`
	Mod         ScheduleMod `json:"mod"`
	SeriesKey   string      `json:"series_key,omitempty"`
	OriginKey   string      `json:"origin_key,omitempty"`
	Active      bool        `json:"active"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type ScheduleQuery struct {
	Semester
	TeachingWeek int       `json:"teaching_week,omitempty"`
	From         time.Time `json:"from,omitempty"`
	To           time.Time `json:"to,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	Offset       int       `json:"offset,omitempty"`
}

type ScheduleUpdate struct {
	Mode        string     `json:"mode"`
	StartTime   *time.Time `json:"start_time,omitempty"`
	EndTime     *time.Time `json:"end_time,omitempty"`
	Title       *string    `json:"title,omitempty"`
	Details     *string    `json:"details,omitempty"`
	EventStatus *string    `json:"event_status,omitempty"`
}

type ScheduleRepository interface {
	Create(context.Context, string, Schedule) (Schedule, error)
	Get(context.Context, string, string) (Schedule, error)
	List(context.Context, string, ScheduleQuery) ([]Schedule, error)
	Update(context.Context, string, string, ScheduleUpdate) (Schedule, error)
	Delete(context.Context, string, string) error
}

type TimetableRefresh struct {
	Semester
	Schedules []Schedule
}

type TimetableResult struct {
	Semester
	Records              int      `json:"records"`
	Generated            int      `json:"generated"`
	SkippedPractice      int      `json:"skipped_practice"`
	RetiredModifications []string `json:"retired_modifications"`
}

// TimetableCommitter atomically archives a complete snapshot and refreshes its schedules.
// It is optional so unrelated adapters/repositories retain their existing contract.
type TimetableCommitter interface {
	CommitTimetable(context.Context, PlatformLease, []PlatformRecord, []byte, TimetableRefresh) (TimetableResult, error)
}
