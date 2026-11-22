package runcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func (s *Service) processFirstCourses(ctx context.Context, b model.ControlBatch, first model.FirstRunRepository) error {
	store, ok := s.Store.(model.ControlItemRepository)
	if !ok {
		return model.ErrUnsupported
	}
	packet, e := first.FirstInput(ctx, b.OwnerID, b.ID)
	if e != nil {
		return e
	}
	if len(packet.Targets) == 0 {
		return f.Fail("NO_SELECTED_COURSES", "请先导入当前学期选课列表，再启动首轮")
	}
	a, e := s.findAgent(ctx, b.OwnerID, "learner")
	if e != nil {
		return e
	}

	// Each course owns a separate staging session. Only the fan-out changes;
	// the Learner's saved concurrency and the model provider still bound it.
	jobs := make(chan model.FirstCourse, len(packet.Targets))
	for _, course := range packet.Targets {
		jobs <- course
	}
	close(jobs)
	results := make(chan error, len(packet.Targets))
	var wg sync.WaitGroup
	for range min(a.Limits.Defaults().MaxConcurrent, s.Agents.Runtime.MaxConcurrent, len(packet.Targets)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for course := range jobs {
				if ctx.Err() != nil {
					return
				}
				results <- s.processFirstCourse(ctx, b, store, a.ID, packet, course)
			}
		}()
	}
	wg.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		return err
	}
	var courseErr error
	for err := range results {
		if err != nil {
			courseErr = err
		}
	}
	return courseErr
}

func saveWaitingFirstCourse(ctx context.Context, store model.ControlItemRepository, item model.ControlItem, status, summary string, verified bool, runErr error) error {
	applied := runErr == nil && status == "APPLIED" && verified
	if applied {
		item.Status = "COMPLETED"
		item.Reason = summary
	}
	if err := store.SaveItem(ctx, item); err != nil {
		return err
	}
	if !applied {
		return firstCourseWaiting()
	}
	return nil
}
func firstCourseWaiting() error {
	return f.Fail("FIRST_COURSES_INCOMPLETE", "课程建档尚未完成，等待已保存问题的答复；不会重复启动调查")
}

func (s *Service) processFirstCourse(ctx context.Context, b model.ControlBatch, store model.ControlItemRepository, learnerID string, packet model.FirstPacket, course model.FirstCourse) error {
	id := stableItemID(b.ID, course.Course.Ref)
	existing, e := store.GetItem(ctx, id)
	if e == nil && (existing.Status == "COMPLETED" || existing.Status == "IGNORED") {
		return nil
	}
	if e != nil && !errors.Is(e, model.ErrNotFound) {
		return e
	}
	// A claimed answer no longer appears in the pending list. Its original
	// item remains waiting until the continuation reconciles a validated result.
	if e == nil && existing.Status == "NEEDS_USER_INPUT" {
		return firstCourseWaiting()
	}
	item := model.ControlItem{ID: id, BatchID: b.ID, RoundID: b.RoundID, OwnerID: b.OwnerID, Source: b.Source, Resource: "selected_course", Title: course.Course.Title, Status: "RUNNING", Reason: "按选课独立调查", Input: mustJSON(course.Course), Evidence: mustJSON([]string{course.Course.Ref})}
	if waiting, err := s.pendingItem(ctx, &item); err != nil {
		return err
	} else if waiting {
		if err := store.SaveItem(ctx, item); err != nil {
			return err
		}
		return firstCourseWaiting()
	}
	if e = store.SaveItem(ctx, item); e != nil {
		return e
	}
	res, runErr := s.Agents.FirstCourse(s.bindContinuation(context.WithValue(ctx, itemKey{}, id), b, id), b.OwnerID, learnerID, id, packet, course)
	var result struct {
		Status, Summary string
		Verified        bool
	}
	_ = json.Unmarshal([]byte(res.Output), &result)
	if waiting, err := s.pendingItem(ctx, &item); err != nil {
		return err
	} else if waiting {
		// Only a server-verified commit meets the first-run gate while an
		// independent follow-up question remains. Preserve its IDs either way.
		return saveWaitingFirstCourse(ctx, store, item, result.Status, result.Summary, result.Verified, runErr)
	}
	item.Status = "FAILED"
	item.Reason = "未取得有效课程建档结果"
	switch {
	case runErr != nil:
		item.Reason = f.SafeError(runErr).Message
	case result.Status == "APPLIED" && result.Verified:
		item.Status = "COMPLETED"
		item.Reason = result.Summary
	case result.Status == "NO_CHANGE":
		item.Status = "IGNORED"
		item.Reason = "本轮没有需要新增或修改的课程记忆"
	}
	if e = store.SaveItem(context.WithoutCancel(ctx), item); e != nil {
		return e
	}
	if item.Status == "FAILED" {
		return f.Fail("FIRST_COURSES_INCOMPLETE", "部分课程建档失败；已完成课程不会重复运行")
	}
	return nil
}
