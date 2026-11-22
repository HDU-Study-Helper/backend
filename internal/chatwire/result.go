// Package chatwire defines only the backend conversation envelope. Cards are
// opaque, inert presentation placeholders: no client renderer is installed.
package chatwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	f "academic/agent/framework"
)

type AnswerOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Interaction struct {
	ID           string         `json:"question_id,omitempty"`
	Type         string         `json:"type"`
	Question     string         `json:"question"`
	Options      []AnswerOption `json:"options,omitempty"`
	RelatedEvent *string        `json:"related_event_id,omitempty"`
	RelatedRun   *string        `json:"related_run_id,omitempty"`
}
type Result struct {
	Text        string            `json:"text"`
	Cards       []json.RawMessage `json:"cards"`
	Interaction *Interaction      `json:"interaction"`
}

func Decode(raw string) (Result, error) {
	var v Result
	bad := errors.New("Chat requires {text,cards,interaction}; cards are inert object placeholders and interaction is null or one question")
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 3 || fields["text"] == nil || fields["cards"] == nil || fields["interaction"] == nil {
		return v, bad
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(&struct{}{}) != io.EOF || v.Cards == nil || len(v.Cards) > 20 || strings.TrimSpace(v.Text) == "" || len(v.Text) > 64<<10 {
		return v, bad
	}
	for _, c := range v.Cards {
		if len(c) > 16<<10 || len(c) == 0 || c[0] != '{' {
			return v, bad
		}
	}
	if q := v.Interaction; q != nil {
		if strings.TrimSpace(q.Question) == "" || len(q.Question) > 4096 || len(q.Options) > 40 {
			return v, bad
		}
		switch q.Type {
		case "CHOICE":
			if len(q.Options) == 0 {
				return v, bad
			}
		case "TEXT_INPUT", "CONFIRM":
		default:
			return v, bad
		}
		seen := map[string]bool{}
		for _, op := range q.Options {
			if strings.TrimSpace(op.ID) == "" || len(op.ID) > 128 || seen[op.ID] || strings.TrimSpace(op.Label) == "" || len(op.Label) > 512 {
				return v, bad
			}
			seen[op.ID] = true
		}
	}
	return v, nil
}
func ValidateOutput(ctx context.Context, in f.OutputValidationInput) (*f.OutputValidationFailure, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if _, e := Decode(in.Output); e != nil {
		return &f.OutputValidationFailure{Kind: f.OutputFormat, Code: "INVALID_CHAT_RESULT", Feedback: e.Error()}, nil
	}
	return nil, nil
}
