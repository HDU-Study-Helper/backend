package model

import (
	"context"
	"encoding/json"
	"time"
)

type ControlRound struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Results    json.RawMessage `json:"results"`
}
type ControlChange struct {
	ID            string          `json:"change_id"`
	Operation     string          `json:"operation"`
	Resource      string          `json:"resource"`
	Source        string          `json:"source"`
	ConnectionID  string          `json:"connection_id,omitempty"`
	GroupID       string          `json:"group_id,omitempty"`
	RecordID      string          `json:"record_id"`
	Before        json.RawMessage `json:"before,omitempty"`
	After         json.RawMessage `json:"after,omitempty"`
	ChangedFields []string        `json:"changed_fields,omitempty"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

// ControlBatchScope records the frozen scheduling unit of a sealed batch.
// CourseRef identifies the enrolled teaching class/term, never the shared source term.
type ControlBatchScope struct {
	Kind         string `json:"kind"` // course, unassociated, group or unresolved_group
	ConnectionID string `json:"connection_id"`
	GroupID      string `json:"group_id,omitempty"`
	OfferingID   string `json:"offering_id,omitempty"`
	CourseRef    string `json:"course_ref,omitempty"`
	CourseName   string `json:"course_name,omitempty"`
}

type ControlBatch struct {
	ID        string             `json:"id"`
	RoundID   string             `json:"round_id"`
	OwnerID   string             `json:"owner_user_id"`
	Source    string             `json:"source"`
	Scope     *ControlBatchScope `json:"scope,omitempty"`
	Status    string             `json:"status"`
	Stage     string             `json:"stage"`
	Count     int                `json:"change_count"`
	Attempt   int                `json:"attempt"`
	Changes   []ControlChange    `json:"changes,omitempty"`
	Trigger   json.RawMessage    `json:"trigger_output"`
	Filter    json.RawMessage    `json:"filter_output"`
	Error     string             `json:"error_code,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}
type ControlRun struct {
	LeaseOwner     string          `json:"-"`
	ID             string          `json:"run_id"`
	BatchID        string          `json:"batch_id"`
	ItemID         string          `json:"item_id,omitempty"`
	OwnerID        string          `json:"owner_user_id"`
	AgentID        string          `json:"agent_id"`
	AgentName      string          `json:"agent_name"`
	WorkflowID     string          `json:"workflow_id,omitempty"`
	WorkflowName   string          `json:"workflow_name,omitempty"`
	ParentID       string          `json:"parent_run_id,omitempty"`
	State          string          `json:"state"`
	BusinessStatus string          `json:"business_status,omitempty"`
	ModelCalls     int             `json:"model_calls"`
	ToolCalls      int             `json:"tool_calls"`
	LastTool       string          `json:"last_tool,omitempty"`
	ToolExecutions json.RawMessage `json:"tool_executions,omitempty"`
	Error          string          `json:"error_code,omitempty"`
	Output         string          `json:"output,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
}
type ControlItem struct {
	ID               string          `json:"item_id"`
	ParentID         string          `json:"parent_item_id,omitempty"`
	BatchID          string          `json:"batch_id"`
	RoundID          string          `json:"round_id"`
	ChangeID         string          `json:"change_id"`
	OwnerID          string          `json:"owner_user_id"`
	Source           string          `json:"source"`
	Resource         string          `json:"resource"`
	Title            string          `json:"title"`
	Status           string          `json:"status"`
	Reason           string          `json:"reason"`
	FilterRunID      string          `json:"filter_run_id,omitempty"`
	ContinuationRuns []string        `json:"continuation_run_ids,omitempty"`
	PendingQuestions []string        `json:"pending_question_ids,omitempty"`
	ReportedID       string          `json:"reported_item_id,omitempty"`
	IDNormalized     bool            `json:"item_reference_normalized,omitempty"`
	Routes           json.RawMessage `json:"routes"`
	Evidence         json.RawMessage `json:"evidence_refs"`
	Input            json.RawMessage `json:"input,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
type ControlArtifact struct {
	ID          string          `json:"artifact_id"`
	ItemID      string          `json:"item_id"`
	BatchID     string          `json:"batch_id"`
	RoundID     string          `json:"round_id"`
	RunID       string          `json:"run_id"`
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	EntityRef   string          `json:"entity_ref,omitempty"`
	Data        json.RawMessage `json:"data"`
	CreatedAt   time.Time       `json:"created_at"`
}
type ControlListQuery struct {
	RoundID, BatchID, ItemID, Status, Type, After string
	Limit, Offset                                 int
}
type ControlItemRepository interface {
	SaveItem(context.Context, ControlItem) error
	GetItem(context.Context, string) (ControlItem, error)
	ListItems(context.Context, ControlListQuery) ([]ControlItem, error)
	SaveArtifact(context.Context, ControlArtifact) error
	ListArtifacts(context.Context, ControlListQuery) ([]ControlArtifact, error)
	ItemCounts(context.Context, string) (map[string]int, error)
}
type ControlOverview struct {
	Mode       string            `json:"mode"`
	Rounds     []ControlRound    `json:"rounds"`
	Batches    []ControlBatch    `json:"batches"`
	Runs       []ControlRun      `json:"runs"`
	Counts     map[string]int    `json:"counts"`
	Items      []ControlItem     `json:"items"`
	Artifacts  []ControlArtifact `json:"artifacts"`
	ItemCounts map[string]int    `json:"item_counts"`
}
type ControlRepository interface {
	StartRound(context.Context, string) (string, error)
	FinishRound(context.Context, string, []RefreshResult, bool) error
	Seal(context.Context) error
	Activate(context.Context, string, string, string, bool) (int, error)
	Claim(context.Context, string) (ControlBatch, error)
	Heartbeat(context.Context, string) error
	SetBatch(context.Context, ControlBatch) error
	GetBatch(context.Context, string) (ControlBatch, error)
	Overview(context.Context) (ControlOverview, error)
	SaveRun(context.Context, ControlRun) error
	GetRun(context.Context, string) (ControlRun, error)
	BatchRuns(context.Context, string) ([]ControlRun, error)
}

// ControlRoundHeartbeater renews only one owned collection round, without
// performing the worker heartbeat's global stale-work housekeeping.
// It is optional so existing ControlRepository implementations remain compatible.
type ControlRoundHeartbeater interface {
	HeartbeatRound(context.Context, string, string) error
}

type controlRoundKey struct{}

func WithControlRound(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, controlRoundKey{}, id)
}
func ControlRoundID(ctx context.Context) string {
	id, _ := ctx.Value(controlRoundKey{}).(string)
	return id
}
