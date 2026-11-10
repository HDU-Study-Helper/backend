package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

func TestPostgresControlCourseOver5000WithinBudget(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "course-tiny", model.RoleUser)
	conn := activeAcademicConnection(t, db, u.ID, "chaoxing")
	ref := controlCourseArchive(t, db, u.ID, "chaoxing", conn, "c", "a", "")
	insertAcademicFixture(t, db, u.ID, conn, academicFixture(2, "chaoxing", "material", "file", ref, "File", nil))
	must(t, db.transaction(ctx, func(tx pgx.Tx) error { return syncAcademicConnection(ctx, tx, u.ID, conn) }))
	var record string
	must(t, db.pool.QueryRow(ctx, `SELECT id::text FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND external_id='file'`, u.ID, conn).Scan(&record))
	round, err := db.Control().StartRound(ctx, "test")
	must(t, err)
	rows := make([][]any, 5001)
	size := 0
	for i := range rows {
		id := fmt.Sprintf("tiny%d", i)
		raw := academicJSON(map[string]any{"change_id": id, "record_id": record, "resource": "material", "source": "chaoxing", "after": map[string]any{"title": "File"}})
		size += len(raw)
		rows[i] = []any{id, round, u.ID, "chaoxing", raw}
	}
	if size > ControlCourseBatchBytes {
		t.Fatal("fixture must fit course budget", size)
	}
	_, err = db.pool.CopyFrom(ctx, pgx.Identifier{"control_events"}, []string{"id", "round_id", "owner_id", "source", "payload"}, pgx.CopyFromRows(rows))
	must(t, err)
	must(t, db.Control().FinishRound(ctx, round, nil, false))
	must(t, db.Control().Seal(ctx))
	must(t, db.Control().Seal(ctx))
	batches := controlCourseBatches(t, db, round)
	if len(batches) != 1 || batches[0].Count != 5001 || batches[0].Status != "pending" || batches[0].Scope.CourseRef != ref || batches[0].Scope.ConnectionID != conn {
		t.Fatalf("complete fitting course not pending: %+v", batches)
	}
}

func TestPostgresControlCourseMigrationPreservesHistoricBatches(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "course-migration", model.RoleUser)
	// Recreate only the pre-V15 surface inside this test's isolated schema.
	_, err := db.pool.Exec(ctx, `ALTER TABLE control_batches DROP COLUMN batch_scope; DELETE FROM dbbridge_migrations WHERE version=15`)
	must(t, err)
	round, err := db.Control().StartRound(ctx, "test")
	must(t, err)
	changes := json.RawMessage(`[{"change_id":"historic","resource":"message","after":{"title":"Historic"}}]`)
	_, err = db.pool.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,status,changes,change_count,trigger_output,error_code)VALUES('historic',$1,$2,'chaoxing','failed',$3,1,'{"decision":"TRIGGER"}','OLD_ERROR')`, round, u.ID, changes)
	must(t, err)
	must(t, db.Migrate(ctx))
	must(t, db.Migrate(ctx))
	b, err := db.Control().GetBatch(ctx, "historic")
	must(t, err)
	if b.Scope != nil || b.Status != "failed" || b.Error != "OLD_ERROR" || b.Count != 1 || b.Changes[0].ID != "historic" || string(b.Trigger) != `{"decision": "TRIGGER"}` {
		t.Fatalf("historic batch rewritten: %+v", b)
	}
	var n int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM dbbridge_migrations`).Scan(&n))
	if n != 15 {
		t.Fatal("migration ledger", n)
	}
}
