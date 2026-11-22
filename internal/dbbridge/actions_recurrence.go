package dbbridge

import (
	"academic/backend/internal/model"
	"time"
)

// Skip missed weeks and nonexistent DST wall times. Do not replay an outage as a burst.
func nextWeekly(in model.ActionInput, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil || in.Due == nil {
		return time.Time{}, model.ErrInvalidArgument
	}
	first := in.Due.In(loc)
	base := time.Date(first.Year(), first.Month(), first.Day(), 12, 0, 0, 0, time.UTC)
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.UTC)
	weeks := (today.Unix() - base.Unix()) / (7 * 86400)
	if weeks < 1 {
		weeks = 1
	}
	for i := int(weeks); i < int(weeks)+4; i++ {
		d := base.AddDate(0, 0, 7*i)
		y, m, day := d.Date()
		v := time.Date(y, m, day, first.Hour(), first.Minute(), first.Second(), first.Nanosecond(), loc)
		if v.Day() == day && v.Hour() == first.Hour() && v.Minute() == first.Minute() && v.After(now) {
			return v.UTC(), nil
		}
	}
	return time.Time{}, model.ErrInvalidArgument
}
