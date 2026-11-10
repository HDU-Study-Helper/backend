package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"context"
)

// Reload before every adapter page so a saved policy applies to stale cursors and
// pending body reads. The storage commit checks again to fence in-flight updates.
func (s *Service) collectionContext(ctx context.Context, scope model.DWSScope) (context.Context, error) {
	repo, ok := s.repo.(model.CollectionPolicyRepository)
	if !ok {
		return ctx, nil
	}
	refs, err := repo.ExcludedCourseRefs(ctx, scope)
	if err != nil {
		return ctx, err
	}
	return contract.WithExcludedCourses(ctx, adapterScope(scope), refs), nil
}
