package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func stableItemID(batch, change string) string {
	h := sha256.Sum256([]byte(batch + "/" + change))
	return "item_" + hex.EncodeToString(h[:16])
}
func newControlItem(b model.ControlBatch, c model.ControlChange) model.ControlItem {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(c.After, &body)
	if body.Title == "" {
		_ = json.Unmarshal(c.Before, &body)
	}
	if body.Title == "" {
		body.Title = c.Resource + "变化"
	}
	raw, _ := json.Marshal(c)
	return model.ControlItem{ID: stableItemID(b.ID, c.ID), BatchID: b.ID, RoundID: b.RoundID, OwnerID: b.OwnerID, ChangeID: c.ID, Source: c.Source, Resource: c.Resource, Title: body.Title, Status: "PENDING", Routes: json.RawMessage(`[]`), Evidence: mustJSON([]string{c.ID}), Input: raw}
}
func mustJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func cleanEvidenceTime(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return raw
	}
	var at string
	if json.Unmarshal(object["observed_at"], &at) == nil && (at == "0001-01-01T00:00:00Z" || at == "0001-01-01T00:00:00+00:00") {
		delete(object, "observed_at")
		return mustJSON(object)
	}
	return raw
}

type routeExecution struct {
	RunID          string      `json:"run_id"`
	Error          *routeError `json:"error"`
	ReportedRunID  string      `json:"reported_run_id,omitempty"`
	Normalized     bool        `json:"run_reference_normalized,omitempty"`
	ServerVerified bool        `json:"server_verified,omitempty"`
	ModelVerified  *bool       `json:"model_verified"`
}
type itemRoute struct {
	ItemID     string          `json:"item_id"`
	Target     string          `json:"target"`
	WorkflowID string          `json:"workflow_id"`
	Execution  *routeExecution `json:"execution"`
}
type itemResult struct {
	ID           string            `json:"item_id"`
	ParentID     string            `json:"parent_item_id"`
	Title        string            `json:"title"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason"`
	Evidence     json.RawMessage   `json:"evidence_refs"`
	Routes       []itemRoute       `json:"routes"`
	Artifacts    []json.RawMessage `json:"artifacts"`
	ReportedID   string            `json:"reported_item_id,omitempty"`
	IDNormalized bool              `json:"item_reference_normalized,omitempty"`
}

func validItemStatus(status string) bool {
	switch status {
	case "IGNORED", "COMPLETED", "NEEDS_USER_INPUT", "PARTIAL", "FAILED":
		return true
	}
	return false
}
func parseItemResults(raw string, parent string) ([]itemResult, error) {
	raw = normalizedResultJSON(raw)
	var out struct {
		Items []itemResult `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if typed, ok := err.(*json.UnmarshalTypeError); ok {
			return nil, f.Fail("INVALID_ITEM_RESULT", fmt.Sprintf("Filter结果字段%s类型错误：应为%s", typed.Field, typed.Type.String()))
		}
		if syntax, ok := err.(*json.SyntaxError); ok {
			return nil, f.Fail("INVALID_ITEM_RESULT", fmt.Sprintf("Filter结果JSON格式错误，偏移%d", syntax.Offset))
		}
		return nil, f.Fail("INVALID_ITEM_RESULT", "Filter结果JSON无法解析")
	}
	if len(out.Items) == 0 {
		return nil, f.Fail("ITEM_COVERAGE_MISSING", "Filter必须返回非空items，逐项说明处理结果")
	}
	if len(out.Items) == 1 {
		item := &out.Items[0]
		if item.ID != "" && item.ID != parent && item.ParentID == "" && len(item.Routes) > 0 {
			consistent := true
			for _, route := range item.Routes {
				if route.ItemID != item.ID {
					consistent = false
				}
			}
			if consistent {
				item.ReportedID = item.ID
				item.IDNormalized = true
				for i := range item.Routes {
					if item.Routes[i].ItemID == item.ID {
						item.Routes[i].ItemID = parent
					}
				}
				item.ID = parent
			}
		}
	}
	seen := map[string]bool{}
	for i := range out.Items {
		v := out.Items[i]
		if v.ID == parent {
			if v.ParentID != "" && v.ParentID != parent {
				return nil, f.Fail("INVALID_ITEM_RESULT", "原始事项不能关联其他父事项")
			}
			out.Items[i].ParentID = ""
			v.ParentID = ""
		}
		if seen[v.ID] || v.ID == "" || !validItemStatus(v.Status) || (v.ID != parent && (v.ParentID != parent || !strings.HasPrefix(v.ID, parent+":"))) {
			return nil, f.Fail("INVALID_ITEM_RESULT", "item_id、父子归属或结果状态无效")
		}
		seen[v.ID] = true
		if strings.TrimSpace(v.Reason) == "" {
			return nil, f.Fail("INVALID_ITEM_RESULT", "每个事项必须给出处理依据")
		}
		for _, r := range v.Routes {
			if r.ItemID != "" && r.ItemID != v.ID {
				return nil, f.Fail("INVALID_ITEM_RESULT", "路由与事项不匹配")
			}
		}
	}
	if len(out.Items) > 1 && seen[parent] {
		return nil, f.Fail("INVALID_ITEM_RESULT", "拆分子事项时不能混合父事项结果")
	}
	return out.Items, nil
}
func (s *Service) processItems(ctx context.Context, b model.ControlBatch, changes []model.ControlChange, store model.ControlItemRepository) (output json.RawMessage, incomplete bool, resultErr error) {
	filter, e := s.findAgent(ctx, b.OwnerID, "filter")
	if e != nil {
		return nil, true, e
	}
	units, e := s.itemWorkUnits(ctx, b, changes, store)
	if e != nil {
		return nil, true, e
	}
	// Also synchronize on early failures/cancellation so the original source
	// rows cannot remain pending after their group has terminated.
	defer func() {
		for _, unit := range units {
			if !unit.grouped() {
				continue
			}
			if _, err := store.GetItem(context.WithoutCancel(ctx), unit.item.ID); err == model.ErrNotFound {
				continue
			}
			if err := syncCourseGroupSources(context.WithoutCancel(ctx), b, unit, store, ""); err != nil && resultErr == nil {
				incomplete, resultErr = true, err
			}
		}
	}()
	summary := []model.ControlItem{}
	partial := false
	for _, unit := range units {
		if e = ctx.Err(); e != nil {
			return mustJSON(map[string]any{"items": summary}), true, e
		}
		change := unit.changes[0]
		item := unit.item
		if existing, err := store.GetItem(ctx, item.ID); err == nil && validItemStatus(existing.Status) {
			// Cancellation marks untouched items FAILED. Resume only those with no
			// recorded execution; an interrupted Filter may already have written data
			// even if its final run ID was never saved on the item.
			untouched := existing.Status == "FAILED" && existing.FilterRunID == "" && existing.Reason == "处理未完成，请查看批次与运行错误"
			if untouched {
				runs, err := s.Store.BatchRuns(ctx, b.ID)
				if err != nil {
					return nil, true, err
				}
				for _, run := range runs {
					if run.ItemID == item.ID {
						untouched = false
						break
					}
				}
			}
			if !untouched {
				summary = append(summary, existing)
				if existing.Status != "IGNORED" && existing.Status != "COMPLETED" {
					partial = true
				}
				continue
			}
		}
		item.Status = "RUNNING"
		if e = store.SaveItem(ctx, item); e != nil {
			return nil, true, e
		}
		if unit.grouped() {
			if e = syncCourseGroupSources(ctx, b, unit, store, "RUNNING"); e != nil {
				return nil, true, e
			}
		}
		input, e := buildWorkItemInput(b, unit)
		if e != nil {
			return nil, true, e
		}
		var payload map[string]any
		_ = json.Unmarshal(input, &payload)
		itemContext := s.enrichItemContext(context.WithValue(ctx, itemKey{}, item.ID), b, change)
		itemContext = f.WithDispatchEvent(itemContext, b.OwnerID, mustJSON(payload))
		result, runErr := s.Agents.Run(itemContext, b.OwnerID, f.RunRequest{AgentID: filter.ID, Trigger: f.DataChanged, Input: mustJSON(payload)})
		item.FilterRunID = result.RunID
		if runErr != nil {
			item.Status = "FAILED"
			item.Reason = f.SafeError(runErr).Code + ": " + f.SafeError(runErr).Message
			partial = true
			_ = store.SaveItem(context.WithoutCancel(ctx), item)
			summary = append(summary, item)
			continue
		}
		parsed, parseErr := parseItemResults(result.Output, item.ID)
		if parseErr != nil {
			priorRuns, loadErr := s.Store.BatchRuns(ctx, b.ID)
			if loadErr == nil {
				children := []model.ControlRun{}
				for _, prior := range priorRuns {
					if prior.ParentID == result.RunID {
						children = append(children, prior)
					}
				}
				correction := map[string]any{"mode": "FORMAT_ONLY", "instruction": "只修正previous_output为合法items JSON；不调用工具、不执行业务、不新增事实。保留原item_id/原业务状态和所有actual_child_runs真实执行ID；不得声称不存在的完成。", "item_id": item.ID, "original_input": payload, "previous_output": result.Output, "actual_child_runs": children, "validation_error": f.SafeError(parseErr).Message}
				repaired, repairErr := s.Agents.Run(itemContext, b.OwnerID, f.RunRequest{AgentID: filter.ID, Trigger: f.SystemEvent, Input: mustJSON(correction), DisableTools: true})
				if repairErr == nil {
					parsed, parseErr = parseItemResults(repaired.Output, item.ID)
				}
			}
		}
		if parseErr != nil {
			item.Status = "FAILED"
			item.Reason = f.SafeError(parseErr).Message
			partial = true
			_ = store.SaveItem(ctx, item)
			summary = append(summary, item)
			continue
		}
		runs, e := s.Store.BatchRuns(ctx, b.ID)
		if e != nil {
			return nil, true, e
		}
		actual := map[string]model.ControlRun{}
		for _, r := range runs {
			if r.ParentID == result.RunID {
				actual[r.ID] = r
			}
		}
		roles := map[string]string{}
		for id, run := range actual {
			if target, err := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: b.OwnerID, AgentID: run.AgentID}); err == nil {
				roles[id] = target.Type
			}
		}
		reported := map[string]bool{}
		children := []model.ControlItem{}
		for _, out := range parsed {
			child := item
			child.ID = out.ID
			child.ParentID = out.ParentID
			child.ReportedID = out.ReportedID
			child.IDNormalized = out.IDNormalized
			child.Status = out.Status
			child.Reason = out.Reason
			if out.Title != "" {
				child.Title = out.Title
			}
			child.Evidence = out.Evidence
			for i := range out.Routes {
				out.Routes[i] = normalizeRouteRun(out.Routes[i], out.ID, b.OwnerID, result.RunID, actual, roles, reported)
			}
			child.Routes = mustJSON(out.Routes)
			issue := ""
			if !s.validateWorkUnitEvidence(itemContext, b.OwnerID, out.Evidence, unit.changes) {
				issue = "证据引用不属于当前输入事项"
			}
			if out.Status == "IGNORED" && len(out.Routes) > 0 {
				issue = "已调度事项不能直接标记忽略"
			}
			workerCompleted := false
			for _, route := range out.Routes {
				if recoveredPreflightFailure(route, out.Routes, actual) {
					continue
				}
				if route.Execution == nil {
					issue = "路由没有真实执行记录"
					continue
				}
				if route.Execution.RunID == "" && route.Execution.Error != nil {
					issue = "工具调用未启动子运行：" + route.Execution.Error.Code
					continue
				}
				run, ok := actual[route.Execution.RunID]
				if !ok || reported[run.ID] {
					issue = "路由重复或未匹配真实子运行"
					continue
				}
				reported[run.ID] = true
				target, err := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: b.OwnerID, AgentID: run.AgentID})
				if err != nil || !strings.EqualFold(route.Target, target.Type) {
					issue = "路由角色不匹配"
					continue
				}
				if run.State != "succeeded" {
					issue = "子运行未成功完成"
				}
				if route.Target == "WORKER" {
					if run.WorkflowID != route.WorkflowID {
						issue = "Workflow与执行不匹配"
						continue
					}
					w, err := s.Agents.Registry.Repository.GetWorkflow(ctx, b.OwnerID, route.WorkflowID)
					if err != nil || w.Status != "active" || !validEntityWorkflow(w.Definition) {
						issue = "Workflow缺少具体实体或不可执行"
						continue
					}
					workerCompleted = workerCompleted || (run.State == "succeeded" && run.BusinessStatus == "COMPLETED")
					if err = store.SaveItem(ctx, child); err != nil {
						return nil, true, err
					}
					artifactItem := child
					if parent, err := store.GetItem(ctx, item.ID); err == nil {
						artifactItem = parent
					}
					s.captureRunArtifacts(ctx, store, artifactItem, run)
				} else if route.Target == "LEARNER" {
					w, modelVerified, err := s.verifyLearnerResult(ctx, b.OwnerID, route, run)
					if err != nil {
						issue = f.SafeError(err).Message
						continue
					}
					route.Execution.ServerVerified = true
					route.Execution.ModelVerified = modelVerified
					if err = store.SaveItem(ctx, child); err != nil {
						return nil, true, err
					}
					_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, "workflow:"+w.ID, 0), ItemID: item.ID, BatchID: b.ID, RoundID: b.RoundID, RunID: run.ID, Type: "WORKFLOW", Title: w.Name, Description: "Learner实际创建或复用的工作流", EntityRef: w.ID, Data: mustJSON(w)})
				} else {
					issue = "未知路由类型"
				}
			}
			if child.Status == "COMPLETED" && !workerCompleted {
				issue = "完成结果缺少真实COMPLETED Worker执行"
			}
			if issue != "" {
				child.Status = "PARTIAL"
				child.Reason += "；" + issue
			}
			if child.Status != "COMPLETED" && child.Status != "IGNORED" {
				partial = true
			}
			if e = store.SaveItem(ctx, child); e != nil {
				return nil, true, e
			}
			// Filter output is routing metadata; only actual Worker/Learner results create artifacts.
			children = append(children, child)
		}
		uncovered := false
		for id := range actual {
			if !reported[id] {
				uncovered = true
			}
		}
		if uncovered {
			partial = true
			for i := range children {
				children[i].Status = "PARTIAL"
				children[i].Reason += "；存在未报告的子运行"
				_ = store.SaveItem(ctx, children[i])
			}
		}
		if len(children) > 1 || children[0].ID != item.ID {
			item.Status = "COMPLETED"
			item.Reason = fmt.Sprintf("已拆为%d个具体事项", len(children))
			allIgnored := true
			for _, child := range children {
				if child.Status != "IGNORED" {
					allIgnored = false
				}
				if child.Status != "COMPLETED" && child.Status != "IGNORED" {
					item.Status = "PARTIAL"
				}
			}
			if allIgnored {
				item.Status = "IGNORED"
			}
			if uncovered {
				item.Status = "PARTIAL"
			}
			if e = store.SaveItem(ctx, item); e != nil {
				return nil, true, e
			}
		}
		summary = append(summary, children...)
	}
	return mustJSON(map[string]any{"items": summary}), partial, nil
}
func recoveredPreflightFailure(route itemRoute, routes []itemRoute, actual map[string]model.ControlRun) bool {
	if route.Execution == nil || route.Execution.RunID != "" || route.Execution.Error == nil || route.Execution.Error.Code != "INVALID_ARGUMENT" {
		return false
	}
	for _, later := range routes {
		if later.Target == route.Target && later.Execution != nil && later.Execution.RunID != "" {
			if run, ok := actual[later.Execution.RunID]; ok && run.State == "succeeded" && (route.WorkflowID == "" || later.WorkflowID == route.WorkflowID) {
				return true
			}
		}
	}
	return false
}
func validEntityWorkflow(raw json.RawMessage) bool { return model.ValidWorkflowEntity(raw) }
func validEvidence(raw json.RawMessage, change model.ControlChange) bool {
	if len(raw) == 0 {
		return true
	}
	var refs []json.RawMessage
	if json.Unmarshal(raw, &refs) != nil {
		return false
	}
	allowed := map[string]bool{}
	for _, ref := range []string{change.ID, change.RecordID, change.ConnectionID} {
		if ref != "" {
			allowed[ref] = true
		}
	}
	var v struct {
		ExternalID string `json:"external_id"`
	}
	_ = json.Unmarshal(change.After, &v)
	if v.ExternalID != "" {
		allowed[v.ExternalID] = true
	}
	for _, raw := range refs {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if !allowed[text] {
				return false
			}
			continue
		}
		var object struct {
			Value    string `json:"value"`
			ID       string `json:"id"`
			RecordID string `json:"record_id"`
		}
		if json.Unmarshal(raw, &object) != nil {
			return false
		}
		if !allowed[object.Value] && !allowed[object.ID] && !allowed[object.RecordID] {
			return false
		}
	}
	return true
}
func artifactID(item, run, kind string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%d", item, run, kind, index)))
	return "artifact_" + hex.EncodeToString(h[:16])
}
func (s *Service) captureRunArtifacts(ctx context.Context, store model.ControlItemRepository, item model.ControlItem, run model.ControlRun) {
	var out struct {
		Artifacts []json.RawMessage `json:"artifacts"`
		Summary   string            `json:"summary"`
	}
	if json.Unmarshal([]byte(run.Output), &out) != nil {
		return
	}
	for i, v := range out.Artifacts {
		var artifact struct {
			Type      string `json:"type"`
			EntityRef string `json:"entity_ref"`
		}
		_ = json.Unmarshal(v, &artifact)
		if artifact.Type == "MEMORY" {
			if artifact.EntityRef == "" {
				continue
			}
			memory, err := s.Agents.Registry.Repository.GetMemory(ctx, f.Scope{OwnerID: item.OwnerID, AgentID: run.AgentID, WorkflowID: run.WorkflowID}, artifact.EntityRef)
			if err != nil {
				continue
			}
			_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, "memory:"+memory.ID, 0), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run.ID, Type: "MEMORY", Title: memory.Title, Description: memory.Content, EntityRef: memory.ID, Data: mustJSON(memory)})
			continue
		}
		s.captureArtifact(ctx, store, item, run.ID, v, i)
	}
	if len(out.Artifacts) == 0 && out.Summary != "" {
		s.captureArtifact(ctx, store, item, run.ID, mustJSON(map[string]any{"type": "SUMMARY", "title": item.Title, "description": out.Summary}), 0)
	}
}
func (s *Service) captureArtifact(ctx context.Context, store model.ControlItemRepository, item model.ControlItem, run string, raw json.RawMessage, index int) {
	var out struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		EntityRef   *string         `json:"entity_ref"`
		Data        json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Title == "" {
		return
	}
	if out.Type == "" {
		out.Type = "SUMMARY"
	}
	ref := ""
	if out.EntityRef != nil {
		ref = *out.EntityRef
	}
	_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run, out.Type, index), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run, Type: out.Type, Title: out.Title, Description: out.Description, EntityRef: ref, Data: out.Data, CreatedAt: time.Now().UTC()})
}
