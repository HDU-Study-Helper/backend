package model

import (
	f "academic/agent/framework"
	"context"
)

// AgentAdminRepository keeps console storage in the existing dbbridge implementation.
// Settings are an opaque encrypted envelope; API keys must never be stored as JSON.
type AgentAdminRepository interface {
	LoadAgentSettings(context.Context) ([]byte, int, error)
	SaveAgentSettings(context.Context, []byte, int) (int, error)
	ListAgentRuns(context.Context, string, string, int) ([]f.RunRecord, error)
}
