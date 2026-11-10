package dbbridge

import (
	"context"
	"sort"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

const collectionPolicyLimit = 2000

var _ model.CollectionPolicyRepository = (*DB)(nil)
var _ model.CollectionPolicyRepository = (*platformRepo)(nil)
var _ model.CollectionPolicyRepository = (*academicRepo)(nil)

func collectionPolicy(ctx context.Context, q academicQuerier, owner string) (model.CollectionPolicy, error) {
	out := model.CollectionPolicy{Excluded: []model.CollectionExclusion{}}
	var disabled bool
	if err := q.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1`, owner).Scan(&disabled); err != nil {
		return out, storageError(err)
	}
	if disabled {
		return out, model.ErrConflict
	}
	err := q.QueryRow(ctx, `SELECT version,updated_at FROM collection_policies WHERE user_id=$1`, owner).Scan(&out.Version, &out.UpdatedAt)
	if err == pgx.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, storageError(err)
	}
	// Deliberately no active/enabled/offering join: disconnected or temporarily
	// missing courses remain visible and removable through their saved snapshots.
	rows, err := q.Query(ctx, `SELECT offering_id,source,connection_id,course_ref,name,native_class_id,native_term_id FROM collection_exclusions WHERE user_id=$1 ORDER BY source,connection_id,course_ref`, owner)
	if err != nil {
		return out, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v model.CollectionExclusion
		if err = rows.Scan(&v.OfferingID, &v.Source, &v.ConnectionID, &v.CourseRef, &v.Name, &v.NativeClassID, &v.NativeTermID); err != nil {
			return out, storageError(err)
		}
		out.Excluded = append(out.Excluded, v)
	}
	return out, storageError(rows.Err())
}

func (d *DB) CollectionPolicy(ctx context.Context, owner string) (out model.CollectionPolicy, err error) {
	if owner == "" {
		return out, model.ErrInvalidArgument
	}
	// A repeatable snapshot prevents a reader combining one policy version with a
	// different replacement set while a concurrent save commits.
	tx, e := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, storageError(e)
	}
	defer tx.Rollback(context.Background())
	out, err = collectionPolicy(ctx, tx, owner)
	if err != nil {
		return out, err
	}
	return out, storageError(tx.Commit(ctx))
}

func collectionSelectionIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	// Bound the submitted input as well as the deduplicated set.
	if len(ids) > collectionPolicyLimit {
		return nil, model.ErrInvalidArgument
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 256 {
			return nil, model.ErrInvalidArgument
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (d *DB) SaveCollectionPolicy(ctx context.Context, owner string, expected int, ids []string) (out model.CollectionPolicy, err error) {
	if owner == "" || expected < 0 {
		return out, model.ErrInvalidArgument
	}
	selected, e := collectionSelectionIDs(ids)
	if e != nil {
		return out, e
	}
	err = d.transaction(ctx, func(tx pgx.Tx) error {
		// platformLock takes this same row lock before writing raw records, derived
		// projections, and captured events. A fetch completed before this save must
		// therefore recheck the committed replacement set before writing anything.
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&disabled); e != nil {
			return storageError(e)
		}
		if disabled {
			return model.ErrConflict
		}
		current, e := collectionPolicy(ctx, tx, owner)
		if e != nil {
			return e
		}
		if current.Version != expected {
			return model.ErrConflict
		}
		retained := map[string]model.CollectionExclusion{}
		for _, v := range current.Excluded {
			retained[v.OfferingID] = v
		}
		resolved := map[string]model.CollectionExclusion{}
		rows, e := tx.Query(ctx, `SELECT o.id,o.source,o.connection_id,o.course_ref,o.name,o.native_class_id,o.native_term_id FROM academic_offerings o JOIN platform_connections c ON c.user_id=o.user_id AND c.id=o.connection_id AND c.source=o.source WHERE o.user_id=$1 AND o.id=ANY($2::text[]) AND c.enabled AND c.state='active' AND o.source IN ('chaoxing','mooc') AND o.course_ref<>'' FOR SHARE OF c`, owner, selected)
		if e != nil {
			return storageError(e)
		}
		for rows.Next() {
			var v model.CollectionExclusion
			if e = rows.Scan(&v.OfferingID, &v.Source, &v.ConnectionID, &v.CourseRef, &v.Name, &v.NativeClassID, &v.NativeTermID); e != nil {
				rows.Close()
				return storageError(e)
			}
			if _, duplicate := resolved[v.OfferingID]; duplicate {
				rows.Close()
				return model.ErrInvalidArgument
			}
			resolved[v.OfferingID] = v
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return storageError(e)
		}
		exclusions := make([]model.CollectionExclusion, 0, len(selected))
		for _, id := range selected {
			v, ok := resolved[id]
			if !ok {
				v, ok = retained[id]
			}
			// An unknown or foreign ID invalidates the entire save, including a mixed
			// valid/invalid set. Only this owner's previously saved rules may be retained.
			if !ok {
				return model.ErrInvalidArgument
			}
			exclusions = append(exclusions, v)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO collection_policies(user_id,version) VALUES($1,1) ON CONFLICT(user_id) DO UPDATE SET version=collection_policies.version+1,updated_at=clock_timestamp()`, owner); e != nil {
			return storageError(e)
		}
		if _, e = tx.Exec(ctx, `DELETE FROM collection_exclusions WHERE user_id=$1`, owner); e != nil {
			return storageError(e)
		}
		for _, v := range exclusions {
			if _, e = tx.Exec(ctx, `INSERT INTO collection_exclusions(user_id,connection_id,source,course_ref,offering_id,name,native_class_id,native_term_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, owner, v.ConnectionID, v.Source, v.CourseRef, v.OfferingID, v.Name, v.NativeClassID, v.NativeTermID); e != nil {
				return storageError(e)
			}
		}
		out, e = collectionPolicy(ctx, tx, owner)
		return e
	})
	return out, err
}

func (d *DB) ExcludedCourseRefs(ctx context.Context, scope model.DWSScope) ([]string, error) {
	if scope.UserID == "" || scope.ConnectionID == "" {
		return nil, model.ErrInvalidArgument
	}
	var source string
	if e := d.pool.QueryRow(ctx, `SELECT c.source FROM platform_connections c JOIN users u ON u.id=c.user_id WHERE c.user_id=$1 AND c.id=$2 AND NOT u.disabled`, scope.UserID, scope.ConnectionID).Scan(&source); e != nil {
		return nil, storageError(e)
	}
	return excludedCourseRefs(ctx, d.pool, scope.UserID, scope.ConnectionID, source)
}
func excludedCourseRefs(ctx context.Context, q academicQuerier, owner, connection, source string) ([]string, error) {
	rows, e := q.Query(ctx, `SELECT course_ref FROM collection_exclusions WHERE user_id=$1 AND connection_id=$2 AND source=$3 ORDER BY course_ref`, owner, connection, source)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ref string
		if e = rows.Scan(&ref); e != nil {
			return nil, storageError(e)
		}
		out = append(out, ref)
	}
	return out, storageError(rows.Err())
}

func (r *platformRepo) CollectionPolicy(ctx context.Context, owner string) (model.CollectionPolicy, error) {
	return r.db.CollectionPolicy(ctx, owner)
}
func (r *platformRepo) SaveCollectionPolicy(ctx context.Context, owner string, version int, ids []string) (model.CollectionPolicy, error) {
	return r.db.SaveCollectionPolicy(ctx, owner, version, ids)
}
func (r *platformRepo) ExcludedCourseRefs(ctx context.Context, s model.DWSScope) ([]string, error) {
	return r.db.ExcludedCourseRefs(ctx, s)
}
func (r *academicRepo) CollectionPolicy(ctx context.Context, owner string) (model.CollectionPolicy, error) {
	return r.db.CollectionPolicy(ctx, owner)
}
func (r *academicRepo) SaveCollectionPolicy(ctx context.Context, owner string, version int, ids []string) (model.CollectionPolicy, error) {
	return r.db.SaveCollectionPolicy(ctx, owner, version, ids)
}
func (r *academicRepo) ExcludedCourseRefs(ctx context.Context, s model.DWSScope) ([]string, error) {
	return r.db.ExcludedCourseRefs(ctx, s)
}
