package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDataQueryBytePaginationDoesNotSkipRecords(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "byte-page", model.RoleUser)
	for i := 0; i < 9; i++ {
		_, e := db.pool.Exec(ctx, `INSERT INTO schedules(id,user_id,academic_year,term,teaching_week,start_time,end_time,title,details,source) VALUES($1,$2,2026,1,1,'2026-11-10T00:00:00Z','2026-11-10T01:00:00Z','数学',$3,'USER')`, fmt.Sprintf("row%d", i), u.ID, strings.Repeat("x", 16000))
		must(t, e)
	}
	q := f.Query{Dataset: "schedules", Fields: []string{"id", "details"}, Limit: 100}
	seen := map[string]bool{}
	pages := 0
	for {
		p, e := db.AgentData().Query(ctx, f.Scope{OwnerID: u.ID}, q)
		must(t, e)
		pages++
		for _, row := range p.Rows {
			id := row["id"].(string)
			if seen[id] {
				t.Fatal("duplicate", id)
			}
			seen[id] = true
			if len(row["details"].(string)) != 16000 {
				t.Fatal("field truncated")
			}
		}
		if !p.HasMore {
			break
		}
		if p.NextOffset == nil || *p.NextOffset <= q.Offset {
			t.Fatal("missing exact offset", p)
		}
		q.Offset = *p.NextOffset
		if pages > 9 {
			t.Fatal("loop")
		}
	}
	if len(seen) != 9 || pages < 2 {
		t.Fatal("lost rows or unbounded result", len(seen), pages)
	}
}
