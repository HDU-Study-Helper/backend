package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type academicRepo struct{ db *DB }

func (d *DB) Academic() model.AcademicRepository { return &academicRepo{d} }

// Tree must either return the complete bounded snapshot or an explicit error.
const academicTreeLimit = 20000

var ErrAcademicTreeOverflow = fmt.Errorf("academic tree exceeds 20000 rows; use paginated offering and item queries: %w", model.ErrConflict)

type academicQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validAcademicSource(source string) bool {
	return source == "" || source == "chaoxing" || source == "mooc"
}
func validAcademicKind(kind string) bool {
	switch kind {
	case "", "assignment", "exam", "notice", "discussion", "progress", "course_observation", "material", "chapter", "lesson", "discussion_reply_user":
		return true
	}
	return false
}
func validateAcademicQuery(owner string, q model.AcademicQuery) error {
	if owner == "" || !validAcademicSource(q.Source) || !validAcademicKind(q.Kind) || q.Offset < 0 || q.Limit < 0 || q.Limit > 101 || len(q.Search) > 1000 || len(q.CourseRef) > 4096 || len(q.ConnectionID) > 256 || len(q.OfferingID) > 256 {
		return model.ErrInvalidArgument
	}
	switch q.Status {
	case "", "submitted", "completed", "open", "needs_redo", "unknown":
	default:
		return model.ErrInvalidArgument
	}
	if q.PublishedAfter != nil && q.PublishedBefore != nil && q.PublishedAfter.After(*q.PublishedBefore) {
		return model.ErrInvalidArgument
	}
	if q.DeadlineAfter != nil && q.DeadlineBefore != nil && q.DeadlineAfter.After(*q.DeadlineBefore) {
		return model.ErrInvalidArgument
	}
	return nil
}
func checkAcademicScope(ctx context.Context, db academicQuerier, owner, source, connection string) error {
	if connection == "" {
		return nil
	}
	var found bool
	err := db.QueryRow(ctx, `SELECT true FROM platform_connections c JOIN users u ON u.id=c.user_id WHERE c.user_id=$1 AND c.id=$2 AND ($3='' OR c.source=$3) AND c.source IN ('chaoxing','mooc') AND c.enabled AND c.state='active' AND NOT u.disabled`, owner, connection, source).Scan(&found)
	return storageError(err)
}
func academicLimit(q model.AcademicQuery) int {
	if q.Limit == 0 {
		return 20
	}
	return q.Limit
}
func academicOfferings(ctx context.Context, db academicQuerier, owner string, q model.AcademicQuery, limit int) ([]model.AcademicOffering, error) {
	rows, err := db.Query(ctx, `SELECT to_jsonb(o)||jsonb_build_object('offering_id',o.id,'record_id',o.record_id::text) FROM academic_offerings o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id JOIN users u ON u.id=c.user_id WHERE o.user_id=$1 AND c.enabled AND c.state='active' AND NOT u.disabled AND ($2='' OR o.source=$2) AND ($3='' OR o.connection_id=$3) AND ($4='' OR o.id=$4) AND ($5='' OR o.course_ref=$5) AND ($6='' OR o.name ILIKE '%'||$6||'%' OR o.teacher ILIKE '%'||$6||'%' OR o.description ILIKE '%'||$6||'%' OR o.details::text ILIKE '%'||$6||'%') ORDER BY o.name,o.id LIMIT $7 OFFSET $8`, owner, q.Source, q.ConnectionID, q.OfferingID, q.CourseRef, q.Search, limit, q.Offset)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := []model.AcademicOffering{}
	for rows.Next() {
		var b []byte
		var o model.AcademicOffering
		if err = rows.Scan(&b); err != nil {
			return nil, storageError(err)
		}
		if err = json.Unmarshal(b, &o); err != nil {
			return nil, model.ErrInternal
		}
		out = append(out, o)
	}
	return out, storageError(rows.Err())
}
func academicItems(ctx context.Context, db academicQuerier, owner string, q model.AcademicQuery, limit int) ([]model.AcademicItem, error) {
	rows, err := db.Query(ctx, `SELECT to_jsonb(i)||jsonb_build_object('item_id',i.id,'text',i.body,'published',i.published_time,'updated',i.updated_time,'start',i.start_time,'end',i.end_time,'deadline',i.deadline_time,'personal_state',COALESCE(p.state,'{}'::jsonb),'record_ids',COALESCE((SELECT jsonb_agg(e.record_id::text ORDER BY e.record_id) FROM academic_item_evidence e WHERE e.user_id=i.user_id AND e.connection_id=i.connection_id AND e.item_id=i.id),'[]'::jsonb)) FROM academic_items i JOIN platform_connections c ON c.user_id=i.user_id AND c.id=i.connection_id JOIN users u ON u.id=c.user_id LEFT JOIN academic_item_personal_states p ON p.user_id=i.user_id AND p.connection_id=i.connection_id AND p.item_id=i.id WHERE i.user_id=$1 AND c.enabled AND c.state='active' AND NOT u.disabled AND ($2='' OR i.source=$2) AND ($3='' OR i.connection_id=$3) AND ($4='' OR i.offering_id=$4) AND ($5='' OR i.course_ref=$5) AND ($6='' OR i.kind=$6) AND ($7='' OR i.status=$7) AND ($8='' OR to_tsvector('simple',i.title||' '||i.body) @@ plainto_tsquery('simple',$8) OR i.title ILIKE '%'||$8||'%' OR i.body ILIKE '%'||$8||'%') AND ($9::timestamptz IS NULL OR i.published_at >= $9) AND ($10::timestamptz IS NULL OR i.published_at <= $10) AND ($11::timestamptz IS NULL OR i.deadline_at >= $11) AND ($12::timestamptz IS NULL OR i.deadline_at <= $12) ORDER BY i.published_at DESC NULLS LAST,i.id LIMIT $13 OFFSET $14`, owner, q.Source, q.ConnectionID, q.OfferingID, q.CourseRef, q.Kind, q.Status, q.Search, q.PublishedAfter, q.PublishedBefore, q.DeadlineAfter, q.DeadlineBefore, limit, q.Offset)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := []model.AcademicItem{}
	for rows.Next() {
		var b []byte
		var i model.AcademicItem
		if err = rows.Scan(&b); err != nil {
			return nil, storageError(err)
		}
		if err = json.Unmarshal(b, &i); err != nil {
			return nil, model.ErrInternal
		}
		out = append(out, i)
	}
	return out, storageError(rows.Err())
}
func (r *academicRepo) Offerings(ctx context.Context, owner string, q model.AcademicQuery) ([]model.AcademicOffering, error) {
	if err := validateAcademicQuery(owner, q); err != nil {
		return nil, err
	}
	if err := checkAcademicScope(ctx, r.db.pool, owner, q.Source, q.ConnectionID); err != nil {
		return nil, err
	}
	return academicOfferings(ctx, r.db.pool, owner, q, academicLimit(q))
}
func (r *academicRepo) Items(ctx context.Context, owner string, q model.AcademicQuery) ([]model.AcademicItem, error) {
	if err := validateAcademicQuery(owner, q); err != nil {
		return nil, err
	}
	if err := checkAcademicScope(ctx, r.db.pool, owner, q.Source, q.ConnectionID); err != nil {
		return nil, err
	}
	return academicItems(ctx, r.db.pool, owner, q, academicLimit(q))
}
func (r *academicRepo) Tree(ctx context.Context, owner, source, connection string) (tree model.AcademicTree, err error) {
	q := model.AcademicQuery{Source: source, ConnectionID: connection}
	if err = validateAcademicQuery(owner, q); err != nil {
		return
	}
	tx, e := r.db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return tree, storageError(e)
	}
	defer tx.Rollback(context.Background())
	if err = checkAcademicScope(ctx, tx, owner, source, connection); err != nil {
		return
	}
	tree.Source = source
	tree.ConnectionID = connection
	tree.Offerings, err = academicOfferings(ctx, tx, owner, q, academicTreeLimit+1)
	if err != nil {
		return
	}
	tree.Items, err = academicItems(ctx, tx, owner, q, academicTreeLimit+1)
	if err != nil {
		return
	}
	if len(tree.Offerings) > academicTreeLimit || len(tree.Items) > academicTreeLimit {
		return model.AcademicTree{}, ErrAcademicTreeOverflow
	}
	tree.Coverage = academicJSON(map[string]any{"scope": "archived_current_records", "complete_tree": true, "source_completeness": "not_asserted", "limit": academicTreeLimit})
	err = storageError(tx.Commit(ctx))
	return
}
func (r *academicRepo) Rebuild(ctx context.Context, owner, source string) (result model.AcademicRebuildResult, err error) {
	result.Source = source
	if owner == "" || !validAcademicSource(source) {
		return result, model.ErrInvalidArgument
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&disabled); e != nil {
			return storageError(e)
		}
		if disabled {
			return model.ErrConflict
		}
		rows, e := tx.Query(ctx, `SELECT id FROM platform_connections WHERE user_id=$1 AND enabled AND state='active' AND source IN ('chaoxing','mooc') AND ($2='' OR source=$2) ORDER BY id FOR UPDATE`, owner, source)
		if e != nil {
			return storageError(e)
		}
		connections := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return storageError(e)
			}
			connections = append(connections, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return storageError(e)
		}
		for _, connection := range connections {
			if e = syncAcademicConnection(ctx, tx, owner, connection); e != nil {
				return e
			}
			result.Connections++
		}
		for _, counter := range []struct {
			sql   string
			value *int
		}{{`SELECT count(*) FROM academic_offerings o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id WHERE o.user_id=$1 AND c.enabled AND c.state='active' AND ($2='' OR c.source=$2)`, &result.Offerings}, {`SELECT count(*) FROM academic_items o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id WHERE o.user_id=$1 AND c.enabled AND c.state='active' AND ($2='' OR c.source=$2)`, &result.Items}, {`SELECT count(*) FROM academic_items o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id WHERE o.user_id=$1 AND c.enabled AND c.state='active' AND o.offering_id IS NULL AND ($2='' OR c.source=$2)`, &result.Unassociated}, {`SELECT count(*) FROM platform_records o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id WHERE o.user_id=$1 AND c.enabled AND c.state='active' AND c.source IN ('chaoxing','mooc') AND ($2='' OR c.source=$2)`, &result.Records}} {
			if e = tx.QueryRow(ctx, counter.sql, owner, source).Scan(counter.value); e != nil {
				return storageError(e)
			}
		}
		return nil
	})
	if err != nil {
		return model.AcademicRebuildResult{Source: source}, err
	}
	return
}
