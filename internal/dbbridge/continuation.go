package dbbridge

import (
	f "academic/agent/framework"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

var _ c.Store = (*agents)(nil)

func (r *agents) SaveContinuation(ctx context.Context, a c.Archive, q *c.Question) error {
	return r.tx(ctx, func(tx pgx.Tx) error { return saveContinuationTx(ctx, tx, a, q) })
}
func saveContinuationTx(ctx context.Context, tx pgx.Tx, a c.Archive, q *c.Question) error {
	t := a.Transcript
	if !a.Source.Valid(t.Owner) || t.Run == "" || t.Agent == "" {
		return f.Fail("INVALID_ARGUMENT", "invalid continuation archive")
	}
	if q != nil && (q.ID == "" || q.RunID != t.Run || q.Role != a.Role || q.Channel != a.Source.Channel || q.Key != a.Source.Key || q.Text != q.Question || q.Text == "") {
		return f.Fail("INVALID_ARGUMENT", "question and archive identity mismatch")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := activeAgent(ctx, tx, f.Scope{OwnerID: t.Owner, AgentID: t.Agent}); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent_continuation_archive(owner_user_id,run_id,archive) SELECT owner_user_id,run_id,$3 FROM agent_runs WHERE owner_user_id=$1 AND run_id=$2 AND agent_id=$4`, t.Owner, t.Run, raw, t.Agent)
	if err != nil {
		return agentError(err)
	}
	if tag.RowsAffected() != 1 {
		return f.Fail("PERMISSION_DENIED", "archive does not belong to the originating run")
	}

	if q != nil {
		if a.Source.Channel == c.ChA {
			if _, e := RecordQuestionNotice(ctx, tx, model.ActionScope{OwnerID: t.Owner, AgentID: t.Agent, RunID: t.Run}, q.ID, q.Text); e != nil {
				return e
			}
		}
		v, e := json.Marshal(q)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO agent_continuation_questions(owner_user_id,question_id,channel,source_key,run_id,question) VALUES($1,$2,$3,$4,$5,$6)`, t.Owner, q.ID, a.Source.Channel, a.Source.Key, t.Run, v)
		return agentError(e)
	}
	return nil
}
func (r *agents) LoadContinuation(ctx context.Context, owner, run string) (a c.Archive, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		var raw []byte
		if e := tx.QueryRow(ctx, `SELECT archive FROM agent_continuation_archive WHERE owner_user_id=$1 AND run_id=$2`, owner, run).Scan(&raw); e != nil {
			return agentError(e)
		}
		return json.Unmarshal(raw, &a)
	})
	return
}
func (r *agents) PendingContinuations(ctx context.Context, s c.Source) (out []c.Question, err error) {
	out = []c.Question{}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, s.Owner); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT question FROM agent_continuation_questions WHERE owner_user_id=$1 AND channel=$2 AND (source_key=$3 OR ($2='ChA' AND $3='')) AND status='pending' ORDER BY created_at,question_id`, s.Owner, s.Channel, s.Key)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			var q c.Question
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &q); e != nil {
				return e
			}
			out = append(out, q)
		}
		return agentError(rows.Err())
	})
	return
}
func (r *agents) CancelContinuations(ctx context.Context, s c.Source) error {
	if s.Channel != c.ChB {
		return f.Fail("PERMISSION_DENIED", "only chat tasks can supersede questions")
	}
	return r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, s.Owner); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE agent_continuation_questions SET status='cancelled' WHERE owner_user_id=$1 AND channel=$2 AND source_key=$3 AND status='pending'`, s.Owner, s.Channel, s.Key)
		return agentError(e)
	})
}

// Never retry an in-flight claim implicitly: a crashed run may already have
// committed external writes. A completed (including failed) answer is replayed
// as a receipt only, without a second model execution.
func (r *agents) ClaimContinuation(ctx context.Context, s c.Source, id, answer string) (q c.Question, done *f.RunResult, err error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(answer)))
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, s.Owner); e != nil {
			return e
		}
		var raw, result []byte
		var status, prev string
		if e := tx.QueryRow(ctx, `SELECT question,status,coalesce(answer_hash,''),result FROM agent_continuation_questions WHERE owner_user_id=$1 AND channel=$2 AND source_key=$3 AND question_id=$4 FOR UPDATE`, s.Owner, s.Channel, s.Key, id).Scan(&raw, &status, &prev, &result); e != nil {
			return agentError(e)
		}
		if e := json.Unmarshal(raw, &q); e != nil {
			return e
		}
		if status == "completed" && prev == digest {
			done = new(f.RunResult)
			return json.Unmarshal(result, done)
		}
		if status != "pending" {
			return f.Fail("CONFLICT", "question is no longer pending")
		}
		if q.Type == "CHOICE" {
			valid := false
			for _, op := range q.Options {
				if answer == op.ID || answer == op.Label {
					valid = true
					break
				}
			}
			if !valid {
				return f.Fail("INVALID_ARGUMENT", "answer is not one of the offered choices")
			}
		}
		_, e := tx.Exec(ctx, `UPDATE agent_continuation_questions SET status='running',answer_hash=$3,answer_text=$4 WHERE owner_user_id=$1 AND question_id=$2`, s.Owner, id, digest, answer)
		return agentError(e)
	})
	return
}
func (r *agents) FinishContinuation(ctx context.Context, s c.Source, id string, out f.RunResult) error {
	v, e := json.Marshal(out)
	if e != nil {
		return e
	}
	return r.tx(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE agent_continuation_questions SET status='completed',result=$5 WHERE owner_user_id=$1 AND channel=$2 AND source_key=$3 AND question_id=$4 AND status='running'`, s.Owner, s.Channel, s.Key, id, v)
		if e != nil {
			return agentError(e)
		}
		if tag.RowsAffected() != 1 {
			return f.Fail("CONFLICT", "question claim no longer available")
		}
		return nil
	})
}
