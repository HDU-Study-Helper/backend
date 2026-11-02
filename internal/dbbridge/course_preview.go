package dbbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"academic/backend/internal/model"
)

// Group only explicit upstream fields. Account-level exams/notices with no course remain unassociated.
const courseIDExpr = `COALESCE(content->'fields'->>'course_id','')`
const classIDExpr = `COALESCE(content->'fields'->>'class_id','')`

func coursePreviewKey(course, class string) string {
	b, _ := json.Marshal([]string{course, class})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCourseKey(key string) (string, string, error) {
	if len(key) > 4096 {
		return "", "", model.ErrInvalidArgument
	}
	b, e := base64.RawURLEncoding.DecodeString(key)
	if e != nil {
		return "", "", model.ErrInvalidArgument
	}
	var pair []string
	if json.Unmarshal(b, &pair) != nil || len(pair) != 2 || pair[0] == "" || pair[1] == "" || strings.ContainsAny(pair[0]+pair[1], "\x00") {
		return "", "", model.ErrInvalidArgument
	}
	return pair[0], pair[1], nil
}
func (r *previewRepo) checkCourseScope(ctx context.Context, owner, source, connection string) error {
	if source != "chaoxing" || owner == "" || connection == "" {
		return model.ErrInvalidArgument
	}
	c, e := r.db.Platforms().Get(ctx, model.DWSScope{UserID: owner, ConnectionID: connection})
	if e != nil {
		return e
	}
	if c.Source != source {
		return model.ErrNotFound
	}
	return nil
}
func (r *previewRepo) Courses(ctx context.Context, owner, source, connection string) ([]model.CoursePreview, int64, error) {
	if e := r.checkCourseScope(ctx, owner, source, connection); e != nil {
		return nil, 0, e
	}
	rows, e := r.db.pool.Query(ctx, `SELECT `+courseIDExpr+` AS course_id,`+classIDExpr+` AS class_id,kind,count(*),count(DISTINCT(external_id,parent_id)),COALESCE((array_agg(content->>'title' ORDER BY id DESC)FILTER(WHERE kind='course' AND COALESCE(content->>'title','')<>''))[1],(array_agg(content->>'title' ORDER BY id DESC)FILTER(WHERE kind='progress' AND COALESCE(content->>'title','')<>''))[1],'') FROM platform_records WHERE user_id=$1 AND connection_id=$2 GROUP BY course_id,class_id,kind ORDER BY course_id,class_id,kind`, owner, connection)
	if e != nil {
		return nil, 0, storageError(e)
	}
	defer rows.Close()
	out := []model.CoursePreview{}
	indices := map[string]int{}
	var unassociated int64
	for rows.Next() {
		var course, class, kind, title string
		var count, entities int64
		if e = rows.Scan(&course, &class, &kind, &count, &entities, &title); e != nil {
			return nil, 0, storageError(e)
		}
		if course == "" || class == "" {
			unassociated += count
			continue
		}
		key := coursePreviewKey(course, class)
		i, ok := indices[key]
		if !ok {
			i = len(out)
			indices[key] = i
			out = append(out, model.CoursePreview{Key: key, CourseID: course, ClassID: class, Resources: []model.ResourcePreview{}})
		}
		v := &out[i]
		if kind == "course" && title != "" {
			v.Title = title
		} else if v.Title == "" && title != "" {
			v.Title = title
		}
		v.RecordCount += count
		v.EntityCount += entities
		v.Resources = append(v.Resources, model.ResourcePreview{Kind: kind, RecordCount: count, EntityCount: entities})
	}
	return out, unassociated, storageError(rows.Err())
}
func (r *previewRepo) CourseRecords(ctx context.Context, owner, source, connection, kind, key string, after int64, limit int) ([]model.PlatformRecord, error) {
	if key == "" {
		return r.Records(ctx, owner, source, connection, kind, after, limit)
	}
	if e := r.checkCourseScope(ctx, owner, source, connection); e != nil {
		return nil, e
	}
	if (kind != "" && !validKind(kind)) || after < 0 || limit < 1 || limit > 101 {
		return nil, model.ErrInvalidArgument
	}
	course, class := "", ""
	if key != "__unassociated__" {
		var e error
		course, class, e = decodeCourseKey(key)
		if e != nil {
			return nil, e
		}
	}
	rows, e := r.db.pool.Query(ctx, `SELECT id,source,kind,external_id,parent_id,hash,content,observed_at FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND ($3='' OR kind=$3) AND id>$4 AND (($5='__unassociated__' AND (`+courseIDExpr+`='' OR `+classIDExpr+`='')) OR ($5<>'__unassociated__' AND `+courseIDExpr+`=$6 AND `+classIDExpr+`=$7)) ORDER BY id LIMIT $8`, owner, connection, kind, after, key, course, class, limit)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.PlatformRecord{}
	for rows.Next() {
		var v model.PlatformRecord
		if e = rows.Scan(&v.ID, &v.Source, &v.Kind, &v.ExternalID, &v.ParentID, &v.Hash, &v.Content, &v.ObservedAt); e != nil {
			return nil, storageError(e)
		}
		out = append(out, v)
	}
	return out, storageError(rows.Err())
}

var _ model.CoursePreviewRepository = (*previewRepo)(nil)
