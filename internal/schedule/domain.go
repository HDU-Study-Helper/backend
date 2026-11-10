// Package schedule implements teaching-week schedules without a background clock.
package schedule

import (
	"strings"
	"time"

	"academic/backend/internal/model"
)

var Location = time.FixedZone("Asia/Shanghai", 8*60*60)

func ValidSemester(s model.Semester) bool {
	return s.AcademicYear >= 2000 && s.AcademicYear <= 2100 && (s.Term == 1 || s.Term == 2)
}
func ValidFields(f model.ScheduleFields) bool {
	return f.TeachingWeek >= 1 && f.TeachingWeek <= 60 && !f.StartTime.IsZero() && f.EndTime.After(f.StartTime) && f.EndTime.Sub(f.StartTime) <= 7*24*time.Hour && f.StartTime.Second() == 0 && f.StartTime.Nanosecond() == 0 && f.EndTime.Second() == 0 && f.EndTime.Nanosecond() == 0 && strings.TrimSpace(f.Title) != "" && len(f.Title) <= 1000 && len(f.Details) <= 16384
}
func TimeStatus(start, end, now time.Time) string {
	if now.Before(start) {
		return "NOT_STARTED"
	}
	if !now.Before(end) {
		return "FINISHED"
	}
	return "IN_PROGRESS"
}
func View(s model.Schedule, now time.Time) model.Schedule {
	s.TimeStatus = TimeStatus(s.StartTime, s.EndTime, now)
	if s.Mod.History == nil {
		s.Mod.History = []model.ScheduleChange{}
	}
	return s
}
func ValidEvent(s string) bool { return s == "INCOMPLETE" || s == "COMPLETED" }

func changed(a, b model.ScheduleFields) []string {
	fields := []string{}
	if !a.StartTime.Equal(b.StartTime) {
		fields = append(fields, "start_time")
	}
	if !a.EndTime.Equal(b.EndTime) {
		fields = append(fields, "end_time")
	}
	if a.Title != b.Title {
		fields = append(fields, "title")
	}
	if a.Details != b.Details {
		fields = append(fields, "details")
	}
	return fields
}
func apply(f model.ScheduleFields, p *model.SchedulePatch) model.ScheduleFields {
	if p == nil {
		return f
	}
	for _, k := range p.Fields {
		switch k {
		case "start_time":
			f.StartTime = p.After.StartTime
		case "end_time":
			f.EndTime = p.After.EndTime
		case "title":
			f.Title = p.After.Title
		case "details":
			f.Details = p.After.Details
		}
	}
	return f
}
func mergePatch(old *model.SchedulePatch, next model.SchedulePatch) *model.SchedulePatch {
	if old == nil {
		return &next
	}
	out := *old
	out.Fields = append([]string{}, old.Fields...)
	out.After = apply(old.After, &next)
	for _, k := range next.Fields {
		found := false
		for _, x := range out.Fields {
			if x == k {
				found = true
			}
		}
		if !found {
			out.Fields = append(out.Fields, k)
		}
	}
	return &out
}
func appendHistory(s *model.Schedule, mode, id string, before, after model.ScheduleFields, now time.Time) error {
	if len(s.Mod.History) >= 256 {
		return model.ErrConflict
	}
	s.Mod.History = append(s.Mod.History, model.ScheduleChange{Mode: mode, OriginalID: id, Before: before, After: after, EffectiveWeek: s.TeachingWeek, At: now})
	return nil
}

// Edit applies a selected occurrence's change to itself, or to one later
// occurrence of the same TIMETABLE series. A temporary override remains local.
func Edit(s, selected model.Schedule, u model.ScheduleUpdate, now time.Time) (model.Schedule, error) {
	if u.Mode != "PERMANENT" && u.Mode != "TEMPORARY" {
		return s, model.ErrInvalidArgument
	}
	if u.EventStatus != nil && !ValidEvent(*u.EventStatus) {
		return s, model.ErrInvalidArgument
	}
	before := s.ScheduleFields
	after := before
	delta := time.Duration(s.TeachingWeek-selected.TeachingWeek) * 7 * 24 * time.Hour
	if u.StartTime != nil {
		after.StartTime = u.StartTime.Add(delta)
	}
	if u.EndTime != nil {
		after.EndTime = u.EndTime.Add(delta)
	}
	if u.Title != nil {
		after.Title = *u.Title
	}
	if u.Details != nil {
		after.Details = *u.Details
	}
	if !ValidFields(after) {
		return s, model.ErrInvalidArgument
	}
	if s.Source == "TIMETABLE" {
		base := s.Mod.Base
		if base == nil {
			return s, model.ErrConflict
		}
		d := base.StartTime.In(Location)
		monday := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, Location).AddDate(0, 0, -(int(d.Weekday())+6)%7)
		if after.StartTime.Before(monday) || !after.StartTime.Before(monday.AddDate(0, 0, 7)) || after.EndTime.After(monday.AddDate(0, 0, 7)) {
			return s, model.ErrInvalidArgument
		}
	}
	fields := changed(before, after)
	if len(fields) > 0 {
		if err := appendHistory(&s, u.Mode, selected.ID, before, after, now); err != nil {
			return s, err
		}
		if s.Source == "TIMETABLE" {
			p := model.SchedulePatch{Fields: fields, After: after}
			if u.Mode == "TEMPORARY" {
				s.Mod.Temporary = mergePatch(s.Mod.Temporary, p)
			} else {
				s.Mod.Permanent = mergePatch(s.Mod.Permanent, p)
			}
			after = apply(apply(*s.Mod.Base, s.Mod.Permanent), s.Mod.Temporary)
			if !ValidFields(after) {
				return s, model.ErrInvalidArgument
			}
		}
		s.ScheduleFields = after
	}
	if u.EventStatus != nil && s.ID == selected.ID {
		s.EventStatus = *u.EventStatus
	}
	s.UpdatedAt = now
	return View(s, now), nil
}

// Rebase keeps local field overrides for an unchanged source identity. Removed
// identities are retired, including their history, instead of moving overrides
// to an unrelated/new arrangement. The caller returns retired modification IDs.
func Rebase(old, fresh model.Schedule, now time.Time) (model.Schedule, error) {
	if fresh.Mod.Base == nil {
		return old, model.ErrInvalidArgument
	}
	if old.Mod.Base != nil && (len(changed(*old.Mod.Base, *fresh.Mod.Base)) > 0 || old.Mod.Base.TeachingWeek != fresh.Mod.Base.TeachingWeek) {
		if err := appendHistory(&old, "SYNC", old.ID, *old.Mod.Base, *fresh.Mod.Base, now); err != nil {
			return old, err
		}
	}
	old.Mod.Base = fresh.Mod.Base
	old.Mod.WeeksRaw = fresh.Mod.WeeksRaw
	old.Mod.TeachingWeeks = append([]int{}, fresh.Mod.TeachingWeeks...)
	old.Mod.WeekPattern = fresh.Mod.WeekPattern
	old.ScheduleFields = apply(apply(*fresh.Mod.Base, old.Mod.Permanent), old.Mod.Temporary)
	if !ValidFields(old.ScheduleFields) {
		return old, model.ErrConflict
	}
	old.Active = !old.Mod.Deleted
	old.UpdatedAt = now
	return View(old, now), nil
}

// InheritPermanent materializes an existing series policy for a newly observed
// future week. Temporary patches and completion state are never inherited.
func InheritPermanent(fresh, template model.Schedule, now time.Time) (model.Schedule, error) {
	if template.Mod.Permanent == nil || template.TeachingWeek > fresh.TeachingWeek || !SameWeekPattern(fresh, template) {
		return fresh, nil
	}
	p := *template.Mod.Permanent
	p.Fields = append([]string{}, p.Fields...)
	delta := time.Duration(fresh.TeachingWeek-template.TeachingWeek) * 7 * 24 * time.Hour
	p.After.StartTime = p.After.StartTime.Add(delta)
	p.After.EndTime = p.After.EndTime.Add(delta)
	p.After.TeachingWeek = fresh.TeachingWeek
	before := fresh.ScheduleFields
	fresh.Mod.Permanent = &p
	fresh.ScheduleFields = apply(before, &p)
	if !ValidFields(fresh.ScheduleFields) {
		return fresh, model.ErrConflict
	}
	original := template.ID
	for i := len(template.Mod.History) - 1; i >= 0; i-- {
		if template.Mod.History[i].Mode == "PERMANENT" {
			original = template.Mod.History[i].OriginalID
			break
		}
	}
	if err := appendHistory(&fresh, "PERMANENT", original, before, fresh.ScheduleFields, now); err != nil {
		return fresh, err
	}
	return fresh, nil
}
