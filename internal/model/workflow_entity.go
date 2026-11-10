package model

import (
	"encoding/json"
	"strings"
)

// ValidWorkflowEntity validates a concrete, stable business scope for active definitions.
func ValidWorkflowEntity(raw json.RawMessage) bool {
	var definition struct {
		Entity struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"entity"`
	}
	if json.Unmarshal(raw, &definition) != nil {
		return false
	}
	e := definition.Entity
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Name) == "" || len(e.ID) > 1024 || len(e.Name) > 512 {
		return false
	}
	switch e.Kind {
	case "course", "competition", "task", "identity":
	default:
		return false
	}
	for _, name := range []string{"课程", "网课", "考试", "作业", "资料", "通知", "比赛", "任务", "身份", "课程目录", "考试管理", "课程资料目录", "课程管理", "学习管理", "资料管理", "网课管理", "course", "courses", "exam", "exams", "task", "identity", "competition", "online course"} {
		if strings.EqualFold(strings.TrimSpace(e.Name), name) || strings.EqualFold(strings.TrimSpace(e.ID), name) {
			return false
		}
	}
	return true
}
