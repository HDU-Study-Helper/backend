package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

func TestControlItemsArtifactsAndLeafCounts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "item-owner", model.RoleUser)
	r := db.Control()
	round, e := r.StartRound(ctx, "test")
	must(t, e)
	_, e = db.pool.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,changes,change_count) VALUES('batch',$1,$2,'chaoxing','[]',1)`, round, u.ID)
	must(t, e)
	store := r.(model.ControlItemRepository)
	parent := model.ControlItem{ID: "parent", BatchID: "batch", RoundID: round, OwnerID: u.ID, ChangeID: "change", Status: "COMPLETED", Title: "多课通知"}
	parent.ParentID = parent.ID
	must(t, store.SaveItem(ctx, parent))
	root, e := store.GetItem(ctx, parent.ID)
	must(t, e)
	if root.ParentID != "" {
		t.Fatal("self-parent persisted", root)
	}
	rootCounts, e := store.ItemCounts(ctx, round)
	must(t, e)
	if rootCounts["COMPLETED"] != 1 {
		t.Fatal("root disappeared", rootCounts)
	}
	child := parent
	child.ID = "parent:1"
	child.ParentID = "parent"
	child.Status = "IGNORED"
	must(t, store.SaveItem(ctx, child))
	counts, e := store.ItemCounts(ctx, round)
	must(t, e)
	if counts["IGNORED"] != 1 || counts["COMPLETED"] != 0 {
		t.Fatal(counts)
	}
	must(t, store.SaveArtifact(ctx, model.ControlArtifact{ID: "artifact", ItemID: child.ID, BatchID: "batch", RoundID: round, Type: "NOTICE", Title: "应用内通知草案", Data: json.RawMessage(`{"delivered":false}`)}))
	items, e := store.ListItems(ctx, model.ControlListQuery{RoundID: round, Limit: 1})
	must(t, e)
	if len(items) != 1 {
		t.Fatal(items)
	}
	arts, e := store.ListArtifacts(ctx, model.ControlListQuery{ItemID: child.ID})
	must(t, e)
	if len(arts) != 1 || arts[0].Title != "应用内通知草案" {
		t.Fatal(arts)
	}
	child.OwnerID = "other"
	if e = store.SaveItem(ctx, child); e == nil {
		t.Fatal("owner overwrite accepted")
	}
}
