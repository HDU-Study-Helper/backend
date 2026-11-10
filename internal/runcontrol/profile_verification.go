package runcontrol

import (
	f "academic/agent/framework"
	"context"
	"encoding/json"
	"strings"
)

// verifyWorkflowProfile validates stored private knowledge under a host-derived
// scope. Learner JSON never grants access to another Workflow's private memory.
// Legacy definitions without a manifest keep their original behavior.
func (s *Service) verifyWorkflowProfile(ctx context.Context, owner string, w f.Workflow) error {
	var definition struct {
		Profile *struct {
			Version  int      `json:"version"`
			Keywords []string `json:"keywords"`
			Coverage []struct {
				Dataset  string `json:"dataset"`
				Complete bool   `json:"complete"`
			} `json:"search_coverage"`
			Dimensions map[string]struct {
				Status     string            `json:"status"`
				SourceRefs []json.RawMessage `json:"source_refs"`
			} `json:"dimensions"`
		} `json:"profile_manifest"`
	}
	if json.Unmarshal(w.Definition, &definition) != nil {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "Workflow画像声明无效")
	}
	p := definition.Profile
	if p == nil {
		return nil
	}
	if p.Version != 1 || len(p.Keywords) == 0 || len(p.Coverage) == 0 {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "画像缺少关键词或实际检索覆盖")
	}
	covered := map[string]bool{}
	for _, coverage := range p.Coverage {
		covered[coverage.Dataset] = coverage.Complete
		if !coverage.Complete {
			return f.Fail("INVALID_WORKFLOW_PROFILE", "画像关键词检索仍未完成："+coverage.Dataset)
		}
	}
	for _, dataset := range []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"} {
		if !covered[dataset] {
			return f.Fail("INVALID_WORKFLOW_PROFILE", "画像缺少实际检索覆盖数据集："+dataset)
		}
	}
	for _, keyword := range p.Keywords {
		if strings.TrimSpace(keyword) == "" {
			return f.Fail("INVALID_WORKFLOW_PROFILE", "画像关键词为空")
		}
	}
	for _, dim := range []string{"identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation"} {
		d, ok := p.Dimensions[dim]
		if !ok || (d.Status != "known" && d.Status != "partial" && d.Status != "unavailable") {
			return f.Fail("INVALID_WORKFLOW_PROFILE", "画像维度缺少明确覆盖状态："+dim)
		}
		if d.Status == "known" && len(d.SourceRefs) == 0 {
			return f.Fail("INVALID_WORKFLOW_PROFILE", "已知画像维度缺少来源："+dim)
		}
	}
	if w.CreatedBy == "" {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "画像缺少受信创建者")
	}
	memories, err := s.Agents.Registry.Repository.ListMemory(ctx, f.Scope{OwnerID: owner, AgentID: w.CreatedBy, WorkflowID: w.ID}, "", 1000)
	if err != nil {
		return err
	}
	index := false
	facts := false
	for _, m := range memories {
		if m.Visibility != "PRI" || m.WorkflowID != w.ID || m.AgentID != "" || m.Status != "active" {
			continue
		}
		var refs []json.RawMessage
		if json.Unmarshal(m.SourceRefs, &refs) != nil || len(refs) == 0 {
			continue
		}
		if m.Category == "profile_index" {
			index = true
		} else if _, ok := p.Dimensions[m.Category]; ok {
			facts = true
		}
	}
	if !index || !facts {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "Workflow缺少可读取且有来源的私有画像索引或主题记忆")
	}
	return nil
}
