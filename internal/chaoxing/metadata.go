package chaoxing

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Metadata is additive source evidence. Central storage owns conversion to instants;
// a list date lacking a year must never inherit the observation's current year.
func courseFields(co course) map[string]any {
	f := map[string]any{"course_id": co.ID, "class_id": co.Class}
	if co.Name != "" {
		f["course_name"] = co.Name
	}
	return f
}

var sourceDate = regexp.MustCompile(`(?:[0-9]{4}[-/.年])?[0-9]{1,2}[-/.月][0-9]{1,2}日?(?:[ T]+[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?)?(?:Z|[+-][0-9]{2}:[0-9]{2})?`)
var yearDate = regexp.MustCompile(`^[0-9]{4}[-/.年]`)
var epochDate = regexp.MustCompile(`^[0-9]{10}(?:[0-9]{3})?$`)

func sourcePrecision(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if epochDate.MatchString(raw) {
		return "instant", ""
	}
	if !sourceDate.MatchString(raw) {
		return "unknown", "unrecognized_source_format"
	}
	if !yearDate.MatchString(raw) {
		return "unknown", "missing_year"
	}
	if strings.Contains(raw, ":") {
		if strings.Count(raw, ":") >= 2 {
			return "second", ""
		}
		return "minute", ""
	}
	return "day", ""
}
func timeEvidence(f map[string]any, name, raw, source string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	precision, reason := sourcePrecision(raw)
	f[name+"_raw"] = raw
	f[name+"_source"] = source
	f[name+"_precision"] = precision
	if reason != "" {
		f[name+"_reason"] = reason
	} else {
		delete(f, name+"_reason")
	}
}
func missingTime(f map[string]any, name, reason string) {
	if _, ok := f[name+"_raw"]; ok {
		return
	}
	f[name+"_precision"] = "unknown"
	f[name+"_reason"] = reason
}
func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return ""
}
func aliasTime(f map[string]any, name string, keys ...string) {
	for _, key := range keys {
		if value := scalarText(f[key]); value != "" {
			timeEvidence(f, name, value, key)
			return
		}
	}
	missingTime(f, name, "not_provided_by_source")
}
func aliasAuthor(f map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := f[key].(string); ok && strings.TrimSpace(value) != "" {
			f["author"] = strings.TrimSpace(value)
			f["author_source"] = key
			return
		}
	}
}

// assignmentMetadata reads visible list labels only. Safe, identity-validated
// task/header enrichment is performed separately by assignment_landing.go.
func assignmentMetadata(n *html.Node, f map[string]any) string {
	f["coverage"] = "assignment_list"
	f["body_status"] = "not_collected"
	f["body_reason"] = "task_detail_not_requested"
	f["time_status"] = "unknown"
	var description string
	walk(n, func(c *html.Node) {
		if c == n || c.Type != html.ElementNode || c.Data == "script" || c.Data == "style" || c.Data == "input" || c.Data == "a" || class(c, "overHidden2") || class(c, "right-content") {
			return
		}
		value := text(c)
		if value == "" {
			return
		}
		if class(c, "time") && strings.HasPrefix(value, "剩余") {
			if _, exists := f["deadline_raw"]; !exists {
				f["deadline_raw"], f["deadline_source"], f["deadline_precision"], f["deadline_reason"] = value, "assignment_list:time", "relative", "relative_countdown_only"
				f["time_raw"], f["time_source"], f["time_precision"], f["time_reason"] = value, "assignment_list:time", "relative", "relative_countdown_only"
				f["time_status"] = "provided"
			}
		}
		// Only small labelled blocks are metadata. A whole row mixes labels, title
		// and status; descendants retain each date's explicit meaning.
		if len([]rune(value)) > 500 {
			return
		}
		labels := []struct{ label, name string }{{"发布时间", "published_at"}, {"发布于", "published_at"}, {"开始时间", "start_at"}, {"截止时间", "deadline"}, {"截止日期", "deadline"}, {"结束时间", "end_at"}}
		for _, item := range labels {
			if !strings.HasPrefix(value, item.label) {
				continue
			}
			tail := strings.TrimSpace(strings.TrimLeft(value[len(item.label):], " :："))
			match := sourceDate.FindStringIndex(tail)
			if match != nil && match[0] == 0 {
				raw := tail[:match[1]]
				timeEvidence(f, item.name, raw, "assignment_list:"+item.label)
				f["time_status"] = "provided"
			}
		}
		for _, label := range []string{"作业说明", "作业描述"} {
			if strings.HasPrefix(value, label) {
				v := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(value, label), " :："))
				if v != "" {
					description = v
					f["description_source"] = "assignment_list:" + label
				}
			}
		}
	})
	missingTime(f, "published_at", "not_provided_by_source")
	missingTime(f, "deadline", "not_provided_by_source")
	if description != "" {
		f["body_status"] = "list_description"
		delete(f, "body_reason")
	}
	return description
}
