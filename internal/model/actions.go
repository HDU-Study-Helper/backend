package model

import (
	"context"
	"encoding/json"
	"time"
)

// ActionScope is supplied by the authenticated host, never decoded from tool input.
type ActionScope struct{ OwnerID, AgentID, RunID string }
type ActionInput struct {
	Key          string          `json:"idempotency_key"`
	Target       string          `json:"todo_key,omitempty"`
	Title        string          `json:"title,omitempty"`
	Body         string          `json:"body,omitempty"`
	Due          *time.Time      `json:"due_at,omitempty"`
	RepeatWeekly bool            `json:"repeat_weekly,omitempty"`
	Timezone     string          `json:"timezone,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	ReminderKey  string          `json:"reminder_key,omitempty"`
	IntentTurn   string          `json:"intent_turn_id,omitempty"`
	IntentQuote  string          `json:"intent_quote,omitempty"`
	PreferenceID string          `json:"preference_memory_id,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Evidence     []string        `json:"evidence"`
	Origin       string          `json:"origin_tool,omitempty"` // set by the host handler, not a tool argument
	Kind         string          `json:"job_kind,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	Args         json.RawMessage `json:"arguments,omitempty"`
}
type PublicAction struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	AgentID   string          `json:"source_agent_id"`
	RunID     string          `json:"source_run_id"`
	Input     ActionInput     `json:"input"`
	Result    json.RawMessage `json:"result,omitempty"`
	ResultRef string          `json:"result_ref,omitempty"`
	Updated   time.Time       `json:"updated_at"`
}
type ClaimedAction struct {
	OwnerID string
	PublicAction
}
type ActionRepository interface {
	GetAction(context.Context, string, string, string) (PublicAction, error)
	PutAction(context.Context, ActionScope, string, ActionInput) (PublicAction, error)
	ListActions(context.Context, string, string, int, int) ([]PublicAction, error)
	ChangeAction(context.Context, ActionScope, string, string) (PublicAction, error)
	ClaimAction(context.Context) (ClaimedAction, error)
	FinishAction(context.Context, ClaimedAction, string, string) error
}
