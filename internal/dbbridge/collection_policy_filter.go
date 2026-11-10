package dbbridge

import (
	"context"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

// filterExcludedPlatformRecords runs after platformLock, inside the same
// transaction and before raw records, academic projections, or event snapshots
// are written. The users row lock also serializes SaveCollectionPolicy, including
// policy changes made while an adapter's upstream fetch was in flight.
func filterExcludedPlatformRecords(ctx context.Context, tx pgx.Tx, owner, connection, source string, records []model.PlatformRecord) ([]model.PlatformRecord, error) {
	for _, row := range records {
		if row.Source != source {
			return nil, model.ErrInvalidArgument
		}
	}
	refs, err := excludedCourseRefs(ctx, tx, owner, connection, source)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return records, nil
	}
	excluded := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		excluded[ref] = struct{}{}
	}
	out := make([]model.PlatformRecord, 0, len(records))
	for _, row := range records {
		blocked, err := collectionRecordExcluded(row, excluded)
		if err != nil {
			return nil, err
		}
		if !blocked {
			out = append(out, row)
		}
	}
	return out, nil
}

func collectionRecordExcluded(row model.PlatformRecord, excluded map[string]struct{}) (bool, error) {
	contains := func(ref string) bool { _, ok := excluded[ref]; return ref != "" && ok }
	if row.Kind == "course" && contains(row.ExternalID) {
		return true, nil
	}
	if contains(row.ParentID) {
		return true, nil
	}
	raw, err := decodeAcademicRaw(row)
	if err != nil {
		return false, err
	}
	// Only explicit source course identity is evidence. A notice or account exam
	// with no course reference remains collectible; names, titles and source-term
	// IDs cannot safely identify an owner's teaching class or MOOC route.
	if contains(academicField(raw.fields, "course_ref", "courseRef")) {
		return true, nil
	}
	if row.Source == "chaoxing" {
		course, class, _ := academicNative(raw.fields, row.Source)
		return contains(academicRef(row.Source, course, class, "")), nil
	}
	if row.Source == "mooc" {
		// Route term identifies the enrolled offering. activeTermId/sourceTermId may
		// instead identify content shared by multiple independent enrolled routes.
		routeCourse := academicField(raw.fields, "routeCourseId", "route_course_id")
		routeTerm := academicField(raw.fields, "routeTermId", "route_term_id")
		if routeCourse != "" && routeTerm != "" {
			return contains(academicRef("mooc", routeCourse, "", routeTerm)), nil
		}
		course := academicField(raw.fields, "courseId", "course_id")
		term := academicField(raw.fields, "termId", "term_id")
		return contains(academicRef("mooc", course, "", term)), nil
	}
	return false, nil
}
