package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"testing"
)

func aliasCourseContext() (context.Context, model.ControlChange) {
	change := model.ControlChange{ID: "change", RecordID: "97328", ConnectionID: "connection", Source: "mooc"}
	course := courseEvidenceReference{OwnerID: "owner", ConnectionID: change.ConnectionID, Source: change.Source, RecordID: change.RecordID, CourseRef: "opaque|course=ref&kind=course"}
	ctx := context.WithValue(context.Background(), knownCourseEvidenceKey{}, course)
	return context.WithValue(ctx, knownAcademicOfferingEvidenceKey{}, academicOfferingEvidenceReference{Course: course, ID: academicEvidenceID}), change
}

func TestAcademicOfferingEvidenceAliasesRequireHostIdentity(t *testing.T) {
	ctx, change := aliasCourseContext()
	refs := []any{"academic_offerings:" + academicEvidenceID, map[string]string{"kind": "academic_offerings", "id": academicEvidenceID}, map[string]string{"kind": "academic_offering", "value": academicEvidenceID}, map[string]string{"type": "academic_offering_id", "academic_offering_id": academicEvidenceID}}
	for _, ref := range refs {
		if !(&Service{}).validateItemEvidence(ctx, "owner", mustJSON([]any{ref}), change) {
			t.Fatalf("valid offering rejected: %#v", ref)
		}
		if (&Service{}).validateItemEvidence(context.Background(), "owner", mustJSON([]any{ref}), change) {
			t.Fatalf("offering accepted without host: %#v", ref)
		}
	}
	for _, tc := range []struct {
		name, owner string
		change      model.ControlChange
		ctx         context.Context
		ref         any
	}{
		{name: "owner", owner: "other"},
		{name: "connection", change: model.ControlChange{RecordID: change.RecordID, ConnectionID: "other", Source: change.Source}},
		{name: "source", change: model.ControlChange{RecordID: change.RecordID, ConnectionID: change.ConnectionID, Source: "chaoxing"}},
		{name: "record", change: model.ControlChange{RecordID: "other", ConnectionID: change.ConnectionID, Source: change.Source}},
		{name: "unknown id", ref: "academic_offerings:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "malformed", ref: "academic_offerings:97328"},
		{name: "namespace collision", ref: map[string]string{"kind": "academic_offering_id", "record_id": change.RecordID}},
		{name: "course mismatch", ctx: context.WithValue(ctx, knownCourseEvidenceKey{}, courseEvidenceReference{OwnerID: "owner", ConnectionID: change.ConnectionID, Source: change.Source, RecordID: change.RecordID, CourseRef: "other"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := tc.owner
			if owner == "" {
				owner = "owner"
			}
			c := tc.change
			if c.RecordID == "" {
				c = change
			}
			cctx := tc.ctx
			if cctx == nil {
				cctx = ctx
			}
			ref := tc.ref
			if ref == nil {
				ref = refs[0]
			}
			if (&Service{}).validateItemEvidence(cctx, owner, mustJSON([]any{ref}), c) {
				t.Fatal("invalid offering accepted")
			}
		})
	}
}

type aliasWorkflowRepo struct {
	f.Repository
	workflow f.Workflow
	calls    int
}

func (r *aliasWorkflowRepo) GetWorkflow(_ context.Context, owner, id string) (f.Workflow, error) {
	r.calls++
	if owner != "owner" || id != "wf" {
		return f.Workflow{}, f.Fail("NOT_FOUND", "unknown")
	}
	return r.workflow, nil
}

func TestWorkflowEvidenceExactOwnerCourseAndCurrentVersion(t *testing.T) {
	ctx, change := aliasCourseContext()
	course := knownCourseEvidence(ctx)
	entity := "source=" + course.Source + "|connection=" + course.ConnectionID + "|course_ref=" + course.CourseRef
	for _, tc := range []struct {
		name, owner, ref, status, entity, kind string
		version                                int
		noHost                                 bool
	}{
		{name: "valid"}, {name: "unknown", ref: "workflow:unknown@v1"}, {name: "owner", owner: "other"}, {name: "version", version: 2}, {name: "inactive", status: "disabled"}, {name: "other course", entity: entity + "x"}, {name: "other connection", entity: "source=mooc|connection=other|course_ref=" + course.CourseRef}, {name: "other source", entity: "source=chaoxing|connection=connection|course_ref=" + course.CourseRef}, {name: "other kind", kind: "assignment"}, {name: "no host", noHost: true}, {name: "leading zero", ref: "workflow:wf@v01"}, {name: "missing version", ref: "workflow:wf"}, {name: "query version", ref: "workflow:wf@v1&kind=course"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := tc.owner
			if owner == "" {
				owner = "owner"
			}
			ref := tc.ref
			if ref == "" {
				ref = "workflow:wf@v1"
			}
			status := tc.status
			if status == "" {
				status = "active"
			}
			e := tc.entity
			if e == "" {
				e = entity
			}
			kind := tc.kind
			if kind == "" {
				kind = "course"
			}
			version := tc.version
			if version == 0 {
				version = 1
			}
			repo := &aliasWorkflowRepo{workflow: f.Workflow{ID: "wf", OwnerID: "owner", Version: version, Status: status, Definition: mustJSON(map[string]any{"entity": map[string]string{"kind": kind, "id": e}})}}
			s := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
			cctx := ctx
			if tc.noHost {
				cctx = context.Background()
			}
			got := s.validateItemEvidence(cctx, owner, mustJSON([]string{ref}), change)
			if got != (tc.name == "valid") {
				t.Fatalf("accepted=%v", got)
			}
			if tc.noHost && repo.calls != 0 {
				t.Fatal("queried without host identity")
			}
		})
	}
}

type aliasRecordData struct {
	f.DataService
	row   map[string]any
	calls int
}

func (d *aliasRecordData) Query(_ context.Context, scope f.Scope, q f.Query) (f.QueryResult, error) {
	d.calls++
	if scope.OwnerID != "owner" || q.Dataset != "platform_records" || len(q.Filters) != 2 || q.Filters[0].Value != "connection" || q.Filters[1].Field != "id" || q.Limit != 1 {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "scope")
	}
	return f.QueryResult{Rows: []map[string]any{d.row}}, nil
}

type aliasOfferingData struct {
	enrichmentData
	rows []map[string]any
}

func (d *aliasOfferingData) Catalog(ctx context.Context, scope f.Scope) ([]f.Dataset, error) {
	sets, err := d.enrichmentData.Catalog(ctx, scope)
	return append(sets, f.Dataset{Name: "academic_offerings"}), err
}
func (d *aliasOfferingData) Query(ctx context.Context, scope f.Scope, q f.Query) (f.QueryResult, error) {
	if q.Dataset != "academic_offerings" {
		return d.enrichmentData.Query(ctx, scope, q)
	}
	if scope.OwnerID != "owner" || len(q.Filters) != 2 || q.Filters[0].Value != "connection" || q.Filters[1].Value != `["course","class"]` || q.Limit != 2 {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "scope")
	}
	return f.QueryResult{Rows: d.rows}, nil
}
func TestAcademicOfferingEnrichmentVerifiesReturnedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, id, conn, course string
		duplicate              bool
		want                   bool
	}{
		{name: "verified", id: academicEvidenceID, conn: "connection", course: `["course","class"]`, want: true},
		{name: "missing connection", id: academicEvidenceID, course: `["course","class"]`},
		{name: "other connection", id: academicEvidenceID, conn: "other", course: `["course","class"]`},
		{name: "other course", id: academicEvidenceID, conn: "connection", course: "other"},
		{name: "bad id", id: "forged", conn: "connection", course: `["course","class"]`},
		{name: "ambiguous", id: academicEvidenceID, conn: "connection", course: `["course","class"]`, duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"id": tc.id, "connection_id": tc.conn, "course_ref": tc.course}
			data := &aliasOfferingData{rows: []map[string]any{row}}
			if tc.duplicate {
				data.rows = append(data.rows, row)
			}
			service := &Service{Data: data, Agents: &agent.Service{Registry: f.Registry{Repository: &enrichmentRepo{}}}}
			change := model.ControlChange{RecordID: "10", ConnectionID: "connection", Source: "chaoxing"}
			ctx := service.enrichItemContext(context.Background(), model.ControlBatch{OwnerID: "owner"}, change)
			if got := (&Service{}).validateItemEvidence(ctx, "owner", mustJSON([]string{"academic_offerings:" + academicEvidenceID}), change); got != tc.want {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
}

func TestNativeEvidenceOpaquePunctuationCompatibility(t *testing.T) {
	for _, id := range []string{"native:opaque", "native=opaque", "native:opaque&part=2"} {
		change := model.ControlChange{RecordID: "record", ConnectionID: "connection", After: mustJSON(map[string]string{"external_id": id})}
		if !(&Service{}).validateItemEvidence(context.Background(), "owner", mustJSON([]string{id}), change) {
			t.Fatalf("native identity rejected: %s", id)
		}
	}
}

func TestPlatformRecordCanonicalAliasStrictNamespace(t *testing.T) {
	_, change := aliasCourseContext()
	for _, tc := range []struct {
		name, owner, id, connection, external string
		want                                  bool
	}{
		{name: "numeric", id: "97329", connection: "connection", want: true}, {name: "native", id: "native-record", connection: "connection", want: true}, {name: "wrong owner", owner: "other", id: "97329", connection: "connection"}, {name: "wrong connection", id: "97329", connection: "other"}, {name: "external collision", id: "other", external: "97329", connection: "connection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "97329"
			if tc.name == "native" {
				id = tc.id
			}
			owner := tc.owner
			if owner == "" {
				owner = "owner"
			}
			d := &aliasRecordData{row: map[string]any{"id": tc.id, "external_id": tc.external, "connection_id": tc.connection}}
			got := (&Service{Data: d}).validateItemEvidence(context.Background(), owner, mustJSON([]string{"platform_records:" + id}), change)
			if got != tc.want {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
	ctx := context.WithValue(context.Background(), knownEvidenceKey{}, map[string]bool{"memory:xxx": true, "academic_items:course_ref=anything&kind=course": true})
	for _, ref := range []string{"memory:xxx", "academic_items:course_ref=anything&kind=course"} {
		if (&Service{}).validateItemEvidence(ctx, "owner", mustJSON([]string{ref}), change) {
			t.Fatalf("unknown namespace accepted: %s", ref)
		}
	}
}
