package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

// CollectOnce drains each active automatic source to its current pagination end.
// It preserves committed checkpoints and never retries a failed page. Teaching
// schedules and on-demand discussion reply-user reads are intentionally excluded.
func (s *Service) CollectOnce(ctx context.Context) error {
	return s.CollectOnceWithTimeout(ctx, 30*time.Minute)
}

// CollectOnceWithTimeout permits a bounded maintenance pass while preserving the
// caller's earlier deadline, page budgets, and stop-without-retry behavior.
func (s *Service) CollectOnceWithTimeout(ctx context.Context, d time.Duration) error {
	if d <= 0 || d > 4*time.Hour {
		return contract.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return s.collectOnce(ctx, 20000, 10000)
}
func (s *Service) collectOnce(ctx context.Context, totalLimit, streamLimit int) error {
	pages := 0
	var failures []error
	after := ""
	for {
		targets, err := s.repo.Targets(ctx, after, 100)
		if err != nil {
			for _, src := range []string{"chaoxing", "mooc", "ucampus"} {
				model.RefreshFailure(ctx, src, "target_list_failed")
			}
			return errors.Join(append(failures, err)...)
		}
		for _, connection := range targets {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			adapter := s.adapters[connection.Source]
			if adapter == nil || !connection.Enabled || connection.State != "active" || adapter.Describe().ManualSyncOnly {
				continue
			}
			model.RefreshConnection(ctx, connection.Source)
			scope := scopeOf(connection)
			blocked := false
			for _, resource := range adapter.Describe().Resources {
				streams := []contract.CoursePartition{{}}
				if _, partitioned := adapter.(contract.DiscussionPartitioner); resource == contract.Discussion && partitioned {
					streams, err = s.discussionCourses(ctx, scope)
					if err != nil {
						model.RefreshAttempt(ctx, connection.Source, errorCode(err))
						failures = append(failures, err)
						if stopCollection(err) {
							blocked = true
							break
						}
						continue
					}
				}
				for _, stream := range streams {
					for streamPages := 0; ; streamPages++ {
						if err = ctx.Err(); err != nil {
							return errors.Join(append(failures, err)...)
						}
						if pages >= totalLimit {
							err = fmt.Errorf("collection total page budget exceeded")
							model.RefreshFailure(ctx, connection.Source, "page_budget_exceeded")
							return errors.Join(append(failures, err)...)
						}
						if streamPages >= streamLimit {
							err = fmt.Errorf("collection stream page budget exceeded")
							model.RefreshFailure(ctx, connection.Source, "page_budget_exceeded")
							failures = append(failures, err)
							break
						}
						pages++
						var more bool
						more, err = s.syncPageResult(ctx, scope, resource, stream.CourseID, stream.ClassID, 0, 0)
						code := ""
						if err != nil {
							code = errorCode(err)
							failures = append(failures, err)
						}
						model.RefreshAttempt(ctx, connection.Source, code)
						if err != nil {
							if stopCollection(err) {
								blocked = true
							}
							break
						}
						if !more {
							break
						}
					}
					if blocked {
						break
					}
				}
				if blocked {
					break
				}
			}
		}
		if len(targets) < 100 {
			break
		}
		next := targets[len(targets)-1].ID
		if next == "" || next == after {
			return errors.Join(append(failures, contract.ErrParse)...)
		}
		after = next
	}
	return errors.Join(failures...)
}
func stopCollection(err error) bool {
	return errors.Is(err, contract.ErrAuthRequired) || errors.Is(err, contract.ErrRateLimited) || errors.Is(err, contract.ErrVerificationRequired) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
