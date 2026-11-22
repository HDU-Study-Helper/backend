package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
)

// RecordQuestionNotice is a trusted continuation transaction sink, not a model tool.
// The question's status and its owner-visible silent notice commit or roll back together.
func RecordQuestionNotice(ctx context.Context, tx pgx.Tx, s model.ActionScope, questionID, text string) (string, error) {
	if strings.TrimSpace(questionID) == "" || len(questionID) > 128 {
		return "", model.ErrInvalidArgument
	}
	in := model.ActionInput{Key: "q:" + questionID, Title: "待回答的问题", Body: text, Mode: "silent", Reason: "后台任务等待用户回答", Evidence: []string{"question:" + questionID}}
	if e := in.Validate("notice"); e != nil {
		return "", e
	}
	if e := actionActor(ctx, tx, s); e != nil {
		return "", e
	}
	in.Key = "internal:question:" + questionID
	raw, e := json.Marshal(in)
	if e != nil {
		return "", model.ErrInvalidArgument
	}
	var id string
	e = tx.QueryRow(ctx, `WITH ins AS (INSERT INTO public_actions(owner_id,id,kind,source_agent_id,source_run_id,status,input) VALUES($1,$2,'notice',$3,$4,'recorded',$5) ON CONFLICT(owner_id,id) DO NOTHING RETURNING id) SELECT id FROM ins UNION ALL SELECT id FROM public_actions WHERE owner_id=$1 AND id=$2 AND kind='notice' AND input=$5::jsonb LIMIT 1`, s.OwnerID, "question:"+questionID, s.AgentID, s.RunID, raw).Scan(&id)
	return id, storageError(e)
}
