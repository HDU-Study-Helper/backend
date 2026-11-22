package model

import (
	"encoding/json"
	"strings"
)

// CourseDelivery identifies a derived delivery-mode rule separately from the
// authoritative archived content. It is not a school's confirmed classification.
type CourseDelivery struct {
	Mode      string `json:"mode"`
	Basis     string `json:"basis"`
	IsDefault bool   `json:"is_default"`
	Label     string `json:"label"`
}

// DefaultCourseDelivery applies the user-approved rule only to archived JWXT
// selected-course records. It does not alter content, hash or observation time.
// Any real value or unrecognized field type leaves the mode unclassified.
func DefaultCourseDelivery(record PlatformRecord) *CourseDelivery {
	if record.Source != "jwxt" || record.Kind != "course" {
		return nil
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(record.Content, &body) != nil || body == nil {
		return nil
	}
	raw, exists := body["fields"]
	if !exists {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	for _, key := range []string{"schedule_raw", "location_raw", "location"} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if strings.TrimSpace(string(value)) == "null" {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return nil
		}
		switch strings.TrimSpace(text) {
		case "", "--", "—", "-":
		default:
			return nil
		}
	}
	return &CourseDelivery{Mode: "online", Basis: "default_missing_time_and_location", IsDefault: true, Label: "线上课程（默认）"}
}
