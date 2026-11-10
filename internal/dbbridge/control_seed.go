package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

// SeedRoundItems is idempotent and can backfill a round already sealed by an older process.
func (r *controls) SeedRoundItems(ctx context.Context) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error { return seedControlItems(ctx, tx) })
}
func seedControlItems(ctx context.Context, tx pgx.Tx) error {
	rows, e := tx.Query(ctx, `SELECT `+batchCols+` FROM control_batches b WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(b.changes) c WHERE NOT EXISTS(SELECT 1 FROM control_items i WHERE i.batch_id=b.id AND i.parent_item_id='' AND i.change_id=c->>'change_id')) ORDER BY b.created_at`)
	if e != nil {
		return e
	}
	batches := []model.ControlBatch{}
	for rows.Next() {
		b, e := scanBatch(rows)
		if e != nil {
			rows.Close()
			return e
		}
		batches = append(batches, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, b := range batches {
		for _, change := range b.Changes {
			sum := sha256.Sum256([]byte(b.ID + "/" + change.ID))
			id := "item_" + hex.EncodeToString(sum[:16])
			var title struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(change.After, &title)
			if title.Title == "" {
				_ = json.Unmarshal(change.Before, &title)
			}
			if title.Title == "" {
				title.Title = change.Resource + "变化"
			}
			status, reason := "PENDING", ""
			if b.Status == "blocked" {
				status = "FAILED"
				reason = "输入过大，批次被阻止；原始证据保留"
			}
			input, _ := json.Marshal(change)
			refs, _ := json.Marshal([]string{change.ID})
			item := model.ControlItem{ID: id, BatchID: b.ID, RoundID: b.RoundID, OwnerID: b.OwnerID, ChangeID: change.ID, Source: change.Source, Resource: change.Resource, Title: title.Title, Status: status, Reason: reason, Routes: json.RawMessage(`[]`), Evidence: refs, Input: input}
			raw, _ := json.Marshal(item)
			if _, e = tx.Exec(ctx, `INSERT INTO control_items(item_id,batch_id,round_id,owner_id,parent_item_id,change_id,status,record) VALUES($1,$2,$3,$4,'',$5,$6,$7) ON CONFLICT(item_id) DO NOTHING`, id, b.ID, b.RoundID, b.OwnerID, change.ID, status, raw); e != nil {
				return e
			}
		}
	}
	return nil
}
