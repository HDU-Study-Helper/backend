package model

import (
	"encoding/json"
	"strings"
	"time"
)

// Validate rejects impossible variants even for non-tool callers. Evidence is cited
// business data, not independent verification of model assertions.
func (in ActionInput) Validate(kind string) error {
	bad := ErrInvalidArgument
	text := func(s string, n int) bool {
		return strings.TrimSpace(s) != "" && len(s) <= n && !strings.ContainsRune(s, 0)
	}
	if !text(in.Key, 160) || (strings.HasPrefix(in.Key, "internal:") && !(kind == "todo" && in.Origin == "scheduled_todo" && strings.HasPrefix(in.Key, "internal:job:"))) || len(in.Evidence) == 0 || len(in.Evidence) > 16 || len(in.Body) > 8192 || len(in.Title) > 512 || len(in.Reason) > 2048 {
		return bad
	}
	for _, s := range in.Evidence {
		if !text(s, 512) {
			return bad
		}
	}
	if in.Due != nil && (in.Due.IsZero() || in.Due.Year() < 1 || in.Due.Year() > 9999) {
		return bad
	}
	if in.RepeatWeekly {
		if kind != "job" || !text(in.Timezone, 80) || in.Timezone == "Local" {
			return bad
		}
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			return bad
		}
	} else if in.Timezone != "" {
		return bad
	}
	if in.Mode == "active" {
		if !text(in.IntentTurn, 128) || !text(in.IntentQuote, 1024) || len(in.PreferenceID) > 128 || in.RepeatWeekly && !text(in.PreferenceID, 128) {
			return bad
		}
	} else if in.IntentTurn != "" || in.IntentQuote != "" || in.PreferenceID != "" {
		return bad
	}
	if in.ReminderKey != "" && (!text(in.ReminderKey, 160) || !(kind == "notice" || in.Kind == "Notice")) {
		return bad
	}
	if kind == "todo" {
		if !text(in.Target, 160) || !text(in.Title, 512) || in.Mode != "" || in.Kind != "" || in.Tool != "" || len(in.Args) > 0 || in.Reason != "" {
			return bad
		}
		return nil
	}
	if in.Target != "" {
		return bad
	}
	if kind == "notice" || kind == "job" && in.Kind == "Notice" {
		if kind == "notice" && (in.Due != nil || in.Kind != "") || kind == "job" && in.Due == nil || !text(in.Title, 512) || !text(in.Body, 8192) || !text(in.Reason, 2048) || (in.Mode != "active" && in.Mode != "silent") || in.Tool != "" || len(in.Args) > 0 {
			return bad
		}
		return nil
	}
	if kind != "job" || in.Due == nil || in.Mode != "" || in.Reason != "" {
		return bad
	}
	switch in.Kind {
	case "Worker":
		if in.Tool != "" || len(in.Args) > 0 || !text(in.Body, 8192) {
			return bad
		}
	case "ToolCall":
		if in.Tool == "public_todo_upsert" {
			_, err := in.ScheduledTodo("validate")
			return err
		}
		if in.Tool != "schedule_get" && in.Tool != "public_todo_complete" && in.Tool != "public_todo_list" && in.Tool != "notice_list" {
			return bad
		}
		var args map[string]json.RawMessage
		if len(in.Args) > 1024 || json.Unmarshal(in.Args, &args) != nil || args == nil {
			return bad
		}
		if in.Tool == "schedule_get" || in.Tool == "public_todo_complete" {
			var id string
			if len(args) != 1 || json.Unmarshal(args["id"], &id) != nil || !text(id, 128) {
				return bad
			}
		} else if len(args) != 0 {
			return bad
		}
	default:
		return bad
	}
	return nil
}
