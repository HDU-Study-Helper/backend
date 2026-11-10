package contract

import "context"

type excludedCoursesKey struct{}
type excludedCourses struct {
	scope Scope
	refs  map[string]bool
}

// WithExcludedCourses is trusted host input. Native references are opaque and scoped
// to one connection; adapters consult it before making course-specific requests.
func WithExcludedCourses(ctx context.Context, scope Scope, refs []string) context.Context {
	values := map[string]bool{}
	for _, ref := range refs {
		values[ref] = true
	}
	return context.WithValue(ctx, excludedCoursesKey{}, excludedCourses{scope, values})
}
func CourseExcluded(ctx context.Context, scope Scope, ref string) bool {
	p, ok := ctx.Value(excludedCoursesKey{}).(excludedCourses)
	return ok && p.scope == scope && p.refs[ref]
}
