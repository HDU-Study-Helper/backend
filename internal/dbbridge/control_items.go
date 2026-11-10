package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

const schemaV11 = `
CREATE TABLE control_items(item_id text PRIMARY KEY,batch_id text NOT NULL REFERENCES control_batches(id) ON DELETE CASCADE,round_id text NOT NULL REFERENCES control_rounds(id) ON DELETE CASCADE,owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,parent_item_id text NOT NULL DEFAULT '',change_id text NOT NULL,status text NOT NULL,record jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX control_items_round ON control_items(round_id,status,item_id);
CREATE INDEX control_items_batch ON control_items(batch_id,item_id);
CREATE TABLE control_artifacts(artifact_id text PRIMARY KEY,item_id text NOT NULL REFERENCES control_items(item_id) ON DELETE CASCADE,batch_id text NOT NULL REFERENCES control_batches(id) ON DELETE CASCADE,round_id text NOT NULL REFERENCES control_rounds(id) ON DELETE CASCADE,run_id text NOT NULL DEFAULT '',type text NOT NULL,record jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX control_artifacts_item ON control_artifacts(item_id,artifact_id);
CREATE INDEX control_artifacts_round ON control_artifacts(round_id,type,artifact_id);
`

func (r *controls) SaveItem(ctx context.Context, v model.ControlItem) error {
	if v.ID == "" || v.BatchID == "" || v.OwnerID == "" {
		return model.ErrInvalidArgument
	}
	if v.ParentID == v.ID {
		v.ParentID = ""
	}
	if len(v.Routes) == 0 {
		v.Routes = json.RawMessage(`[]`)
	}
	if len(v.Evidence) == 0 {
		v.Evidence = json.RawMessage(`[]`)
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	tag, e := r.db.pool.Exec(ctx, `INSERT INTO control_items(item_id,batch_id,round_id,owner_id,parent_item_id,change_id,status,record) SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE EXISTS(SELECT 1 FROM control_batches WHERE id=$2 AND round_id=$3 AND owner_id=$4) ON CONFLICT(item_id) DO UPDATE SET status=EXCLUDED.status,parent_item_id=EXCLUDED.parent_item_id,record=EXCLUDED.record,updated_at=now() WHERE control_items.batch_id=EXCLUDED.batch_id AND control_items.owner_id=EXCLUDED.owner_id`, v.ID, v.BatchID, v.RoundID, v.OwnerID, v.ParentID, v.ChangeID, v.Status, raw)
	if e != nil {
		return storageError(e)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrConflict
	}
	return nil
}
func scanControlItem(row pgx.Row) (v model.ControlItem, e error) {
	var raw []byte
	e = row.Scan(&raw, &v.CreatedAt, &v.UpdatedAt)
	if e != nil {
		return v, storageError(e)
	}
	created, updated := v.CreatedAt, v.UpdatedAt
	e = json.Unmarshal(raw, &v)
	v.CreatedAt, v.UpdatedAt = created, updated
	return
}
func (r *controls) GetItem(ctx context.Context, id string) (model.ControlItem, error) {
	return scanControlItem(r.db.pool.QueryRow(ctx, `SELECT record,created_at,updated_at FROM control_items WHERE item_id=$1`, id))
}
func listBounds(q model.ControlListQuery) (int, int) {
	limit := q.Limit
	if limit < 1 || limit > 500 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func (r *controls) ListItems(ctx context.Context, q model.ControlListQuery) (out []model.ControlItem, e error) {
	out = []model.ControlItem{}
	limit, offset := listBounds(q)
	rows, e := r.db.pool.Query(ctx, `SELECT record,created_at,updated_at FROM control_items WHERE ($1='' OR round_id=$1) AND ($2='' OR batch_id=$2) AND ($3='' OR status=$3) AND ($4='' OR item_id>$4) ORDER BY item_id LIMIT $5 OFFSET $6`, q.RoundID, q.BatchID, q.Status, q.After, limit, offset)
	if e != nil {
		return out, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanControlItem(rows)
		if e != nil {
			return out, e
		}
		out = append(out, v)
	}
	return out, storageError(rows.Err())
}
func (r *controls) ItemCounts(ctx context.Context, round string) (map[string]int, error) {
	out := map[string]int{}
	rows, e := r.db.pool.Query(ctx, `SELECT status,count(*) FROM control_items WHERE ($1='' OR round_id=$1) AND NOT EXISTS(SELECT 1 FROM control_items child WHERE child.parent_item_id=control_items.item_id) GROUP BY status`, round)
	if e != nil {
		return out, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if e = rows.Scan(&s, &n); e != nil {
			return out, e
		}
		out[s] = n
	}
	return out, storageError(rows.Err())
}
func (r *controls) SaveArtifact(ctx context.Context, v model.ControlArtifact) error {
	if v.ID == "" || v.ItemID == "" {
		return model.ErrInvalidArgument
	}
	if len(v.Data) == 0 {
		v.Data = json.RawMessage(`{}`)
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = r.db.pool.Exec(ctx, `INSERT INTO control_artifacts(artifact_id,item_id,batch_id,round_id,run_id,type,record) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(artifact_id) DO UPDATE SET record=EXCLUDED.record`, v.ID, v.ItemID, v.BatchID, v.RoundID, v.RunID, v.Type, raw)
	return storageError(e)
}
func (r *controls) ListArtifacts(ctx context.Context, q model.ControlListQuery) (out []model.ControlArtifact, e error) {
	out = []model.ControlArtifact{}
	limit, offset := listBounds(q)
	rows, e := r.db.pool.Query(ctx, `SELECT record,created_at FROM control_artifacts WHERE ($1='' OR round_id=$1) AND ($2='' OR item_id=$2) AND ($3='' OR type=$3) AND ($4='' OR batch_id=$4) AND ($5='' OR artifact_id>$5) ORDER BY artifact_id LIMIT $6 OFFSET $7`, q.RoundID, q.ItemID, q.Type, q.BatchID, q.After, limit, offset)
	if e != nil {
		return out, storageError(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var v model.ControlArtifact
		if e = rows.Scan(&raw, &v.CreatedAt); e != nil {
			return out, e
		}
		at := v.CreatedAt
		if e = json.Unmarshal(raw, &v); e != nil {
			return out, e
		}
		v.CreatedAt = at
		out = append(out, v)
	}
	return out, storageError(rows.Err())
}
