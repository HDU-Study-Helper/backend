package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

func discussionPartitionKey(course, class string) string {
	b, _ := json.Marshal([]string{course, class})
	return "discussion:" + base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) SyncDiscussionCourse(ctx context.Context, scope model.DWSScope, course, class string) error {
	if course == "" || class == "" || len(course) > 256 || len(class) > 256 {
		return contract.ErrInvalid
	}
	return s.syncPage(ctx, scope, contract.Discussion, course, class)
}
func (s *Service) syncDiscussionCourses(ctx context.Context, scope model.DWSScope) error {
	var courses []contract.CoursePartition
	e := s.operate(ctx, scope, "", func(ctx context.Context, l model.PlatformLease, state model.PlatformState, a contract.Adapter) error {
		partitioned, ok := a.(contract.DiscussionPartitioner)
		if !ok {
			return contract.ErrUnsupported
		}
		if state.Connection.State != "active" {
			return contract.ErrAuthRequired
		}
		raw, e := s.vault.Open(scope.UserID, scope.ConnectionID, state.Session)
		if e != nil {
			return e
		}
		defer clear(raw)
		courses, e = partitioned.DiscussionCourses(ctx, adapterScope(scope), raw)
		return e
	})
	if e != nil {
		return e
	}
	var failures []error
	for _, course := range courses {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := s.SyncDiscussionCourse(ctx, scope, course.CourseID, course.ClassID); e != nil {
			failures = append(failures, e)
			if errors.Is(e, contract.ErrAuthRequired) {
				break
			}
		}
	}
	return errors.Join(failures...)
}
