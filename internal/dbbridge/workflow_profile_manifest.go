package dbbridge

import (
	f "academic/agent/framework"
	"encoding/json"
	"strings"
)

// Course workflows created by the new Learner bootstrap must include the actual
// survey manifest. Unknown source facts are allowed; unfinished available search
// pages are not a ready profile. Non-course/legacy creation remains compatible.
func validateInitialCourseProfile(w f.Workflow, seeds []f.Memory) error {
	var d struct {
		Entity struct {
			Kind string `json:"kind"`
		} `json:"entity"`
		Manifest *struct {
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
	if json.Unmarshal(w.Definition, &d) != nil {
		return f.Fail("INVALID_ARGUMENT", "invalid profile definition")
	}
	if d.Entity.Kind != "course" {
		return nil
	}
	p := d.Manifest
	if p == nil || p.Version != 1 || len(p.Keywords) == 0 || len(p.Coverage) == 0 {
		return f.Fail("INVALID_ARGUMENT", "课程私有画像初始化需要profile_manifest：version=1、keywords、search_coverage和六个dimensions；先完成调查，不能仅创建记忆占位。")
	}
	covered := map[string]bool{}
	for _, c := range p.Coverage {
		covered[c.Dataset] = c.Complete
		if !c.Complete {
			return f.Fail("INVALID_ARGUMENT", "关键词检索仍未完成，继续原样游标后再创建课程画像："+c.Dataset)
		}
	}
	for _, dataset := range []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"} {
		if !covered[dataset] {
			return f.Fail("INVALID_ARGUMENT", "课程画像缺少已完成检索数据集："+dataset)
		}
	}
	for _, key := range []string{"identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation"} {
		v, ok := p.Dimensions[key]
		if !ok || (v.Status != "known" && v.Status != "partial" && v.Status != "unavailable") {
			return f.Fail("INVALID_ARGUMENT", "画像维度缺少明确状态："+key)
		}
		if v.Status == "known" && len(v.SourceRefs) == 0 {
			return f.Fail("INVALID_ARGUMENT", "已知画像维度缺少source_refs："+key)
		}
	}
	hasIndex, hasFact := false, false
	for _, m := range seeds {
		if m.Category == "profile_index" {
			hasIndex = true
		}
		if _, ok := p.Dimensions[m.Category]; ok && strings.TrimSpace(m.Content) != "" {
			hasFact = true
		}
	}
	if !hasIndex || !hasFact {
		return f.Fail("INVALID_ARGUMENT", "课程画像必须有profile_index及实际主题记忆")
	}
	return nil
}
