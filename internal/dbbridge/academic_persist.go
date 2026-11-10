package dbbridge

import (
	"context"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

// All table/column names are internal constants. JSON data remains parameterized.
func queueAcademicUpsert(batch *pgx.Batch, table string, columns []string, data map[string]any) {
	updates := make([]string, 0, len(columns))
	for _, column := range columns {
		updates = append(updates, column+"=EXCLUDED."+column)
	}
	batch.Queue(`INSERT INTO `+table+` SELECT (jsonb_populate_record(NULL::`+table+`,$1::jsonb)).* ON CONFLICT(user_id,connection_id,id) DO UPDATE SET `+strings.Join(updates, ",")+` WHERE `+table+` IS DISTINCT FROM EXCLUDED`, []byte(academicJSON(data)))
}

func persistAcademicProjection(ctx context.Context, tx pgx.Tx, owner, connection string, p academicProjection) error {
	// Preserve FK order and the caller's transaction while sending writes together.
	// Close drains every result and reports execution errors before the caller can
	// commit raw records, session or checkpoint. No query runs while a batch is open.
	batch := &pgx.Batch{}
	offeringIDs, courseIDs, itemIDs := []string{}, []string{}, []string{}
	for _, o := range p.offerings {
		courseIDs = append(courseIDs, o.CourseID)
		offeringIDs = append(offeringIDs, o.ID)
		queueAcademicUpsert(batch, "courses", []string{"name"}, map[string]any{"user_id": owner, "connection_id": connection, "id": o.CourseID, "source": o.Source, "native_course_id": o.NativeCourseID, "name": o.Name})
		data := map[string]any{"user_id": owner, "connection_id": connection, "id": o.ID, "course_id": o.CourseID, "source": o.Source, "course_ref": o.CourseRef, "native_course_id": o.NativeCourseID, "native_class_id": o.NativeClassID, "native_term_id": o.NativeTermID, "course_type": o.CourseType, "name": o.Name, "teacher": o.Teacher, "school": o.School, "source_terms": o.SourceTerms, "coverage": o.Coverage, "record_id": o.RecordID, "observed_at": o.ObservedAt, "description": o.Description, "details": o.Details}
		queueAcademicUpsert(batch, "academic_offerings", []string{"course_id", "source", "course_ref", "native_course_id", "native_class_id", "native_term_id", "course_type", "name", "teacher", "school", "source_terms", "coverage", "record_id", "observed_at", "description", "details"}, data)
	}
	for _, i := range p.items {
		itemIDs = append(itemIDs, i.ID)
		var offering any
		if i.OfferingID != "" {
			offering = i.OfferingID
		}
		data := map[string]any{"user_id": owner, "connection_id": connection, "id": i.ID, "source": i.Source, "offering_id": offering, "course_ref": i.CourseRef, "course_name": i.CourseName, "kind": i.Kind, "task_type": i.TaskType, "title": i.Title, "body": i.Text, "author": i.Author, "native_id": i.NativeID, "native_course_id": i.NativeCourseID, "native_class_id": i.NativeClassID, "native_term_id": i.NativeTermID, "source_term_id": i.SourceTermID, "chapter_id": i.ChapterID, "lesson_id": i.LessonID, "unit_id": i.UnitID, "content_id": i.ContentID, "path": i.Path, "published_at": i.Published.Value, "updated_at": i.Updated.Value, "start_at": i.Start.Value, "end_at": i.End.Value, "deadline_at": i.Deadline.Value, "published_time": i.Published, "updated_time": i.Updated, "start_time": i.Start, "end_time": i.End, "deadline_time": i.Deadline, "status": i.Status, "source_status": i.SourceStatus, "metadata": i.Metadata, "missing": i.Missing, "observed_at": i.ObservedAt}
		columns := []string{"source", "offering_id", "course_ref", "course_name", "kind", "task_type", "title", "body", "author", "native_id", "native_course_id", "native_class_id", "native_term_id", "source_term_id", "chapter_id", "lesson_id", "unit_id", "content_id", "path", "published_at", "updated_at", "start_at", "end_at", "deadline_at", "published_time", "updated_time", "start_time", "end_time", "deadline_time", "status", "source_status", "metadata", "missing", "observed_at"}
		queueAcademicUpsert(batch, "academic_items", columns, data)
		batch.Queue(`INSERT INTO academic_item_personal_states(user_id,connection_id,item_id,state) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,connection_id,item_id) DO UPDATE SET state=EXCLUDED.state WHERE academic_item_personal_states.state IS DISTINCT FROM EXCLUDED.state`, owner, connection, i.ID, []byte(i.PersonalState))
		records := []int64{}
		for _, r := range p.evidence[i.ID] {
			records = append(records, r.ID)
			batch.Queue(`INSERT INTO academic_item_evidence(user_id,connection_id,item_id,record_id,resource_kind,external_id,parent_id,logical_id) VALUES($1,$2,$3,$4,$5,$6,$7,$3) ON CONFLICT(user_id,connection_id,item_id,record_id) DO NOTHING`, owner, connection, i.ID, r.ID, r.Kind, r.ExternalID, r.ParentID)
		}
		batch.Queue(`DELETE FROM academic_item_evidence WHERE user_id=$1 AND connection_id=$2 AND item_id=$3 AND NOT(record_id=ANY($4::bigint[]))`, owner, connection, i.ID, records)
	}
	for _, cleanup := range []struct {
		table string
		ids   []string
	}{{"academic_items", itemIDs}, {"academic_offerings", offeringIDs}, {"courses", courseIDs}} {
		batch.Queue(`DELETE FROM `+cleanup.table+` WHERE user_id=$1 AND connection_id=$2 AND NOT(id=ANY($3::text[]))`, owner, connection, cleanup.ids)
	}
	return storageError(tx.SendBatch(ctx, batch).Close())
}

var _ model.AcademicRepository = (*academicRepo)(nil)
