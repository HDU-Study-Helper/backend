package model

import (
	"context"
	"encoding/json"
	"time"
)

type ChatThread struct {
	ID        string    `json:"thread_id"`
	CreatedAt time.Time `json:"created_at"`
}
type ChatTurn struct {
	ID         string          `json:"turn_id"`
	Thread     string          `json:"thread_id"`
	Kind       string          `json:"kind"`
	Message    string          `json:"message"`
	QuestionID string          `json:"question_id,omitempty"`
	State      string          `json:"state"`
	RunID      string          `json:"run_id,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error_code,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}
type ChatStore interface {
	NewChatThread(context.Context, string) (ChatThread, error)
	BeginChatTurn(ctx context.Context, owner, thread, requestID, kind, message, question string) (ChatTurn, bool, error)
	RenewChatTurn(ctx context.Context, owner, thread, turn string) error
	FinishChatTurn(ctx context.Context, owner, thread, turn, run, errCode string, output json.RawMessage) error
	ListChatTurns(ctx context.Context, owner, thread string, limit int) ([]ChatTurn, error)
}
