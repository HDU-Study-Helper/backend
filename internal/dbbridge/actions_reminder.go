package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

func reminderKey(in model.ActionInput) string {
	if in.ReminderKey != "" {
		return in.ReminderKey
	}
	return in.Key
}

// Verify the literal quotation against an authenticated user-authored turn. The
// model, not substring matching, decides semantic relevance. Never authorize
// an active interruption based on urgency alone or an archived memory.
func reminderProof(ctx context.Context, tx pgx.Tx, s model.ActionScope, in model.ActionInput) (*time.Time, error) {
	if in.Mode != "active" {
		return nil, nil
	}
	var one int
	sourceRef := "chat_turn:" + in.IntentTurn
	e := tx.QueryRow(ctx, `SELECT 1 FROM chat_turns WHERE owner_id=$1 AND id=$2 AND kind IN ('message','answer') AND strpos(message,$3)>0 LIMIT 1 FOR SHARE`, s.OwnerID, in.IntentTurn, in.IntentQuote).Scan(&one)
	if errors.Is(e, pgx.ErrNoRows) {
		// Only the authenticated owner's claimed continuation answer is evidence; never
		// quote a generated question, pending choices, or archived model text.
		sourceRef = "question_answer:" + in.IntentTurn
		e = tx.QueryRow(ctx, `SELECT 1 FROM agent_continuation_questions WHERE owner_user_id=$1 AND question_id=$2 AND channel IN ('ChA','ChB') AND status IN ('running','completed') AND answer_hash=encode(sha256(convert_to(answer_text,'UTF8')),'hex') AND strpos(answer_text,$3)>0 FOR SHARE`, s.OwnerID, in.IntentTurn, in.IntentQuote).Scan(&one)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, model.ErrInvalidArgument
	}
	if e != nil {
		return nil, storageError(e)
	}
	if in.PreferenceID == "" {
		return nil, nil
	}
	var updated time.Time
	e = tx.QueryRow(ctx, `SELECT m.updated_at FROM memories m JOIN agent_runs r ON r.owner_user_id=m.owner_user_id AND r.run_id=$3 AND r.agent_id=$4 WHERE m.owner_user_id=$1 AND m.memory_id=$2 AND m.status='active' AND m.is_personal AND (m.valid_from IS NULL OR m.valid_from<=now()) AND (m.valid_until IS NULL OR m.valid_until>now()) AND (m.visibility='PUB' OR m.owner_agent_id=r.agent_id OR (m.owner_workflow_id=r.record->>'workflow_id' AND EXISTS (SELECT 1 FROM agent_workflows w WHERE w.owner_user_id=m.owner_user_id AND w.workflow_id=m.owner_workflow_id AND w.status='active'))) AND m.source_refs ? $5 FOR SHARE OF m`, s.OwnerID, in.PreferenceID, s.RunID, s.AgentID, sourceRef).Scan(&updated)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, model.ErrInvalidArgument
	}
	return &updated, storageError(e)
}

// Canonical reminder identity is independent of agent/run/channel and prose. Hold
// the current occurrence lock while superseding a pending reminder. A running
// occurrence cannot be replaced ambiguously.
func previousReminder(ctx context.Context, tx pgx.Tx, s model.ActionScope, in model.ActionInput, rev *time.Time) (a model.PublicAction, found bool, err error) {
	var id string
	var same bool
	e := tx.QueryRow(ctx, `SELECT action_id,(original_due IS NOT DISTINCT FROM $3::timestamptz AND original_mode=$4 AND repeat_weekly=$5 AND timezone=$6 AND preference_rev IS NOT DISTINCT FROM $7::timestamptz) FROM reminder_keys WHERE owner_id=$1 AND reminder_key=$2`, s.OwnerID, reminderKey(in), in.Due, in.Mode, in.RepeatWeekly, in.Timezone, rev).Scan(&id, &same)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, false, nil
	}
	if e != nil {
		return a, false, storageError(e)
	}
	a, e = scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+` FROM public_actions WHERE owner_id=$1 AND id=$2 FOR UPDATE`, s.OwnerID, id))
	if e != nil {
		return a, false, e
	}
	if same && a.Status != "cancelled" && a.Status != "failed" {
		return a, true, nil
	}
	if a.Status == "running" {
		return a, false, model.ErrConflict
	}
	if a.Status == "pending" {
		_, e = tx.Exec(ctx, `UPDATE public_actions SET status='cancelled',updated_at=now() WHERE owner_id=$1 AND id=$2`, s.OwnerID, id)
	}
	return a, false, storageError(e)
}
