package agent

import (
	"context"

	f "academic/agent/framework"
	"academic/backend/internal/continuation"
	"academic/backend/internal/model"
)

const firstRunPrefix = "首次运行：调查这门选课。数据库动态资料限于最近七天，输入可能不全；只保存有依据且有用的发现，不抄课表、不为缺项追问。没有可保存的信息时可返回 NO_CHANGE，completion 为 null。其余沿用原规则。\n\n"

func (s *Service) FirstCourse(ctx context.Context, owner, id, item string, p model.FirstPacket, course model.FirstCourse) (f.RunResult, error) {
	if p.AsOf.IsZero() || course.Course.Kind != "selected_course" || course.Course.Entity == "" {
		return f.RunResult{}, f.Fail("INVALID_ARGUMENT", "invalid first-run selected course")
	}
	input := map[string]any{"item_id": item, "task": "调查这门选课并建档", "entity": map[string]string{"kind": "course", "id": course.Course.Entity, "name": course.Course.Title}, "selected_course": course.Course, "course_candidates": course.Context, "as_of": p.AsOf, "since": p.Since}
	if _, ok := s.Registry.Repository.(continuation.Store); ok {
		if _, bound := continuation.Current(ctx); !bound {
			ctx = continuation.WithSource(ctx, owner, continuation.ChA, item)
		}
	}
	ctx = model.WithFirstReadWindow(WithLearnerStage(ctx, owner, "INITIALIZE", ""), owner, p.AsOf)
	return s.Run(ctx, owner, f.RunRequest{AgentID: id, Trigger: f.SystemEvent, Input: mustStageJSON(input)})
}

type firstCourseRepo struct{ f.Repository }

func (r firstCourseRepo) GetAgent(ctx context.Context, scope f.Scope) (f.AgentInstance, error) {
	a, err := r.Repository.GetAgent(ctx, scope)
	if err == nil {
		a.SystemPrompt = firstRunPrefix + a.SystemPrompt
	}
	return a, err
}
