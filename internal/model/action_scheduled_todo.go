package model

import (
	"encoding/json"
	"time"
)

// ScheduledTodo decodes only the narrow nested todo contract. Owner, actor,
// occurrence request key and evidence are inherited, not model arguments.
func (in ActionInput) ScheduledTodo(jobID string) (out ActionInput, err error) {
	if in.Kind != "ToolCall" || in.Tool != "public_todo_upsert" || jobID == "" || len(in.Args) > 12288 {
		return out, ErrInvalidArgument
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(in.Args, &fields) != nil {
		return out, ErrInvalidArgument
	}
	for k, v := range fields {
		if k != "todo_key" && k != "title" && k != "body" && k != "due_at" || string(v) == "null" {
			return out, ErrInvalidArgument
		}
	}
	var args struct {
		Key   string     `json:"todo_key"`
		Title string     `json:"title"`
		Body  string     `json:"body"`
		Due   *time.Time `json:"due_at"`
	}
	if json.Unmarshal(in.Args, &args) != nil {
		return out, ErrInvalidArgument
	}
	out = ActionInput{Key: "internal:job:" + jobID, Origin: "scheduled_todo", Target: args.Key, Title: args.Title, Body: args.Body, Due: args.Due, Evidence: in.Evidence}
	return out, out.Validate("todo")
}
