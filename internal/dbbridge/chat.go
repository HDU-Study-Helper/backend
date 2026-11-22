package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

const chatSchemaSQL = `
CREATE TABLE chat_threads(owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(owner_id,id));
CREATE TABLE chat_turns(owner_id text NOT NULL, thread_id text NOT NULL, id text NOT NULL, request_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('message','answer')), message text NOT NULL, question_id text NOT NULL DEFAULT '', state text NOT NULL CHECK(state IN ('running','completed','failed','interrupted')), run_id text NOT NULL DEFAULT '', output jsonb, error_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT clock_timestamp(), heartbeat timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(owner_id,thread_id,id), UNIQUE(owner_id,thread_id,request_id), FOREIGN KEY(owner_id,thread_id) REFERENCES chat_threads(owner_id,id) ON DELETE CASCADE);
CREATE UNIQUE INDEX chat_turn_inflight ON chat_turns(owner_id,thread_id) WHERE state='running';
CREATE INDEX chat_turn_history ON chat_turns(owner_id,thread_id,created_at DESC,id DESC);
`

type chatStore struct{ db *DB }

func (d *DB) Chat() model.ChatStore { return &chatStore{d} }
func (r *chatStore) NewChatThread(ctx context.Context, owner string) (v model.ChatThread, err error) {
	err = storageError(r.db.pool.QueryRow(ctx, `INSERT INTO chat_threads(owner_id,id) SELECT id,$2 FROM users WHERE id=$1 AND NOT disabled RETURNING chat_threads.id,created_at`, owner, newID()).Scan(&v.ID, &v.CreatedAt))
	return
}

const chatCols = `id,thread_id,kind,message,question_id,state,run_id,output,error_code,created_at`

func scanTurn(row pgx.Row) (t model.ChatTurn, err error) {
	var raw []byte
	err = row.Scan(&t.ID, &t.Thread, &t.Kind, &t.Message, &t.QuestionID, &t.State, &t.RunID, &raw, &t.Error, &t.CreatedAt)
	t.Output = raw
	return
}
func (r *chatStore) BeginChatTurn(ctx context.Context, owner, thread, req, kind, msg, question string) (v model.ChatTurn, created bool, err error) {
	if len(req) > 128 || strings.TrimSpace(req) == "" || strings.TrimSpace(msg) == "" || len(msg) > 8192 || len(question) > 128 || !(kind == "message" && question == "" || kind == "answer" && question != "") {
		return v, false, model.ErrInvalidArgument
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT t.id FROM chat_threads t JOIN users u ON u.id=t.owner_id WHERE t.owner_id=$1 AND t.id=$2 AND NOT u.disabled FOR UPDATE OF t`, owner, thread).Scan(&id); e != nil {
			return storageError(e)
		}
		if _, e := tx.Exec(ctx, `UPDATE chat_turns SET state='interrupted',error_code='INTERRUPTED_REVIEW_ACTIONS' WHERE owner_id=$1 AND thread_id=$2 AND state='running' AND heartbeat<clock_timestamp()-interval '2 minutes'`, owner, thread); e != nil {
			return storageError(e)
		}
		var e error
		v, e = scanTurn(tx.QueryRow(ctx, `SELECT `+chatCols+` FROM chat_turns WHERE owner_id=$1 AND thread_id=$2 AND request_id=$3`, owner, thread, req))
		if e == nil {
			if v.Kind != kind || v.Message != msg || v.QuestionID != question {
				return model.ErrConflict
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return storageError(e)
		}
		v, e = scanTurn(tx.QueryRow(ctx, `INSERT INTO chat_turns(owner_id,thread_id,id,request_id,kind,message,question_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,'running') RETURNING `+chatCols, owner, thread, newID(), req, kind, msg, question))
		created = e == nil
		return storageError(e)
	})
	return
}
func (r *chatStore) RenewChatTurn(ctx context.Context, owner, thread, turn string) error {
	tag, e := r.db.pool.Exec(ctx, `UPDATE chat_turns SET heartbeat=clock_timestamp() WHERE owner_id=$1 AND thread_id=$2 AND id=$3 AND state='running'`, owner, thread, turn)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() != 1 {
		return model.ErrConflict
	}
	return nil
}
func (r *chatStore) FinishChatTurn(ctx context.Context, owner, thread, turn, run, code string, output json.RawMessage) error {
	state := "completed"
	if code != "" {
		state = "failed"
	}
	tag, e := r.db.pool.Exec(ctx, `UPDATE chat_turns SET state=$4,run_id=$5,output=$6,error_code=$7,heartbeat=clock_timestamp() WHERE owner_id=$1 AND thread_id=$2 AND id=$3 AND state='running'`, owner, thread, turn, state, run, []byte(output), code)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() != 1 {
		return model.ErrConflict
	}
	return nil
}
func (r *chatStore) ListChatTurns(ctx context.Context, owner, thread string, limit int) (out []model.ChatTurn, err error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidArgument
	}
	var ok bool
	if err = r.db.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_threads t JOIN users u ON u.id=t.owner_id WHERE t.owner_id=$1 AND t.id=$2 AND NOT u.disabled)`, owner, thread).Scan(&ok); err != nil {
		return nil, storageError(err)
	}
	if !ok {
		return nil, model.ErrNotFound
	}
	rows, e := r.db.pool.Query(ctx, `SELECT `+chatCols+` FROM chat_turns WHERE owner_id=$1 AND thread_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3`, owner, thread, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out = []model.ChatTurn{}
	for rows.Next() {
		v, e := scanTurn(rows)
		if e != nil {
			return nil, storageError(e)
		}
		out = append(out, v)
	}
	slices.Reverse(out)
	return out, rows.Err()
}
