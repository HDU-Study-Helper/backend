// Package runcontrol owns the explicitly started manual processing lifecycle.
// Collection persists pending batches; no model is called until an admin starts one.
package runcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
)

type batchKey struct{}
type itemKey struct{}
type Service struct {
	Store    model.ControlRepository
	Agents   *agent.Service
	Data     f.DataService
	instance string
	mu       sync.Mutex
	active   map[string]context.CancelFunc
}

func New(store model.ControlRepository, agents *agent.Service, data ...f.DataService) *Service {
	s := &Service{Store: store, Agents: agents, instance: f.NewID(), active: map[string]context.CancelFunc{}}
	if len(data) > 0 {
		s.Data = data[0]
	}
	agents.Runtime.Observer = s.observe
	return s
}
func (s *Service) observe(ctx context.Context, r f.RunRecord, state, output string) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	batch, _ := ctx.Value(batchKey{}).(string)
	item, _ := ctx.Value(itemKey{}).(string)
	v := model.ControlRun{LeaseOwner: s.instance, ID: r.ID, BatchID: batch, ItemID: item, OwnerID: r.OwnerID, AgentID: r.AgentID, WorkflowID: r.WorkflowID, ParentID: r.ParentRunID, State: state, Output: output, StartedAt: r.StartedAt, UpdatedAt: time.Now().UTC(), FinishedAt: r.FinishedAt, ModelCalls: r.ModelCalls, ToolCalls: r.ToolCalls}
	role := ""
	if a, e := s.Agents.Registry.Repository.GetAgent(c, f.Scope{OwnerID: r.OwnerID, AgentID: r.AgentID}); e == nil {
		v.AgentName = a.Name
		role = a.Type
	}
	if r.WorkflowSnapshot != nil {
		v.WorkflowName = r.WorkflowSnapshot.Name
	}
	if len(r.ToolExecutions) > 0 {
		v.LastTool = r.ToolExecutions[len(r.ToolExecutions)-1].Name
		v.ToolExecutions = mustJSON(r.ToolExecutions)
	}
	if r.Error != nil {
		v.Error = r.Error.Code
		if v.Error == "CANCELLED" {
			v.State = "cancelled"
		}
	}
	var business struct {
		Status string `json:"status"`
	}
	if output != "" {
		if json.Unmarshal([]byte(output), &business) == nil {
			v.BusinessStatus = business.Status
		} else {
			v.BusinessStatus = "INVALID_OUTPUT"
		}
	}
	if e := s.Store.SaveRun(c, v); e != nil {
		slog.Warn("run control telemetry unavailable", "run_id", r.ID)
	}
	if item != "" {
		if store, ok := s.Store.(model.ControlItemRepository); ok {
			if current, err := store.GetItem(c, item); err == nil {
				s.capturePersistedToolArtifacts(c, store, current, v)
				if output != "" && (state == "succeeded" || state == "failed" || state == "cancelled") {
					s.captureTerminalArtifacts(c, store, current, v, role)
				}
			}
		}
	}
}
func (s *Service) findAgent(ctx context.Context, owner, role string) (f.AgentInstance, error) {
	after := ""
	var found f.AgentInstance
	for {
		items, e := s.Agents.Registry.Repository.ListAgents(ctx, owner, 100, after)
		if e != nil {
			return found, e
		}
		for _, a := range items {
			if a.Type == role && a.Status == "enabled" {
				if found.ID != "" {
					return found, f.Fail("AMBIGUOUS_AGENT", "multiple enabled agents with required role")
				}
				found = a
			}
		}
		if len(items) < 100 {
			break
		}
		after = items[len(items)-1].ID
	}
	if found.ID == "" {
		return found, f.Fail("AGENT_NOT_CONFIGURED", "required agent role is not enabled")
	}
	return found, nil
}
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	// Model HTTP concurrency is independently bounded by the provider gate.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					batch, e := s.Store.Claim(ctx, s.instance)
					if errors.Is(e, model.ErrNotFound) {
						continue
					}
					if e != nil {
						slog.Warn("run control claim failed")
						continue
					}
					s.process(ctx, batch)
				}
			}
		}()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-ticker.C:
			if e := s.Store.Seal(ctx); e != nil {
				slog.Warn("run control batch sealing failed")
			}
			if e := s.Store.Heartbeat(ctx, s.instance); e != nil {
				slog.Warn("run control heartbeat failed")
			}
		}
	}
}
func (s *Service) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	cancel := s.active[id]
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if cancel != nil {
		return nil
	}
	b, e := s.Store.GetBatch(ctx, id)
	if e != nil {
		return e
	}
	if b.Status != "queued" {
		return model.ErrConflict
	}
	b.Status = "cancelled"
	b.Error = "CANCELLED_BY_ADMIN"
	return s.Store.SetBatch(ctx, b)
}
func (s *Service) process(parent context.Context, b model.ControlBatch) {
	ctx, cancel := context.WithCancel(context.WithValue(parent, batchKey{}, b.ID))
	s.mu.Lock()
	s.active[b.ID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.active, b.ID); s.mu.Unlock() }()
	finish := func(status, code string) {
		b.Status = status
		b.Error = code
		c, stop := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer stop()
		if e := s.Store.SetBatch(c, b); e != nil {
			slog.Warn("run control batch finalization failed", "batch_id", b.ID)
		}
	}
	fail := func(err error) {
		code := f.SafeError(err).Code
		status := "failed"
		if ctx.Err() != nil {
			status = "cancelled"
			if parent.Err() != nil {
				status = "interrupted"
			}
			code = "CANCELLED_OR_INTERRUPTED_REVIEW_CHILD_RUNS"
		}
		finish(status, code)
	}
	itemsStore, hasItems := s.Store.(model.ControlItemRepository)
	if hasItems {
		for _, change := range b.Changes {
			if _, err := itemsStore.GetItem(ctx, stableItemID(b.ID, change.ID)); err == nil {
				continue
			}
			if e := itemsStore.SaveItem(ctx, newControlItem(b, change)); e != nil {
				fail(e)
				return
			}
		}
	}
	markUnprocessed := func(reason string) {
		if hasItems {
			for _, change := range b.Changes {
				item, e := itemsStore.GetItem(context.WithoutCancel(ctx), stableItemID(b.ID, change.ID))
				if e == nil && (item.Status == "PENDING" || item.Status == "RUNNING") {
					item.Status = "FAILED"
					item.Reason = reason
					_ = itemsStore.SaveItem(context.WithoutCancel(ctx), item)
				}
			}
		}
	}
	// A successful Trigger phase intentionally leaves related items pending while
	// the batch yields its processing slot back to the persistent queue.
	yielded := false
	defer func() {
		if !yielded {
			markUnprocessed("处理未完成，请查看批次与运行错误")
		}
	}()
	itemsPhase := hasItems && b.Stage == "items"
	if !itemsPhase {
		b.Stage = "trigger"
	}
	b.Status = "running"
	if e := s.Store.SetBatch(ctx, b); e != nil {
		fail(e)
		return
	}
	var selected []model.ControlChange
	if string(b.Trigger) != "{}" && len(b.Trigger) > 0 {
		decision, e := validateTrigger(b.Trigger, b.Changes)
		if e != nil {
			fail(e)
			return
		}
		selected = pick(b.Changes, decision.IDs)
	} else {
		if itemsPhase {
			fail(f.Fail("INVALID_TRIGGER_OUTPUT", "逐项阶段缺少已保存的有效Trigger结果"))
			return
		}
		a, e := s.findAgent(ctx, b.OwnerID, "trigger")
		if e != nil {
			fail(e)
			return
		}
		decision, raw, e := s.screenTrigger(ctx, b, a.ID)
		if e != nil {
			fail(e)
			return
		}
		b.Trigger = raw
		selected = pick(b.Changes, decision.IDs)
	}
	if hasItems {
		chosen := map[string]bool{}
		for _, v := range selected {
			chosen[v.ID] = true
		}
		for _, change := range b.Changes {
			if !chosen[change.ID] {
				item := newControlItem(b, change)
				item.Status = "IGNORED"
				item.Reason = "Trigger初筛认为无需后续处理"
				if e := itemsStore.SaveItem(ctx, item); e != nil {
					fail(e)
					return
				}
			}
		}
	}
	if len(selected) == 0 {
		finish("ignored", "")
		return
	}
	if hasItems {
		if !itemsPhase {
			// Initial screening is short and prioritized over potentially long item
			// execution. Persist its verified output before releasing this slot.
			b.Stage = "items"
			b.Status = "queued"
			if e := s.Store.SetBatch(ctx, b); e != nil {
				fail(e)
				return
			}
			yielded = true
			return
		}
		summary, partial, e := s.processItems(ctx, b, selected, itemsStore)
		if e != nil {
			fail(e)
			return
		}
		b.Filter = summary
		if partial {
			finish("partial", "ITEMS_REQUIRE_ATTENTION")
		} else {
			finish("succeeded", "")
		}
		return
	}
	b.Stage = "filter"
	if e := s.Store.SetBatch(ctx, b); e != nil {
		fail(e)
		return
	}
	a, e := s.findAgent(ctx, b.OwnerID, "filter")
	if e != nil {
		fail(e)
		return
	}
	input, e := filterInput(b, selected)
	if e != nil {
		fail(f.Fail("INVALID_FILTER_INPUT", "change evidence could not be encoded"))
		return
	}
	result, e := s.Agents.Run(ctx, b.OwnerID, f.RunRequest{AgentID: a.ID, Trigger: f.DataChanged, Input: input})
	if e != nil {
		fail(e)
		return
	}
	if json.Valid([]byte(result.Output)) {
		b.Filter = json.RawMessage(result.Output)
	}
	var output struct {
		Status string `json:"status"`
		Routes []struct {
			Target     string `json:"target"`
			WorkflowID string `json:"workflow_id"`
			Execution  *struct {
				RunID string `json:"run_id"`
			} `json:"execution"`
		} `json:"routes"`
	}
	if json.Unmarshal([]byte(result.Output), &output) != nil {
		fail(f.Fail("INVALID_FILTER_OUTPUT", "filter did not return JSON"))
		return
	}
	switch output.Status {
	case "ROUTED", "PARTIAL", "NEEDS_INFO", "NO_MATCH", "IGNORED", "ERROR":
	default:
		fail(f.Fail("INVALID_FILTER_OUTPUT", "invalid filter status"))
		return
	}
	if output.Status == "ROUTED" && len(output.Routes) == 0 {
		fail(f.Fail("INVALID_FILTER_OUTPUT", "routed output has no routes"))
		return
	}
	runs, err := s.Store.BatchRuns(ctx, b.ID)
	if err != nil {
		fail(err)
		return
	}
	actual := map[string]model.ControlRun{}
	for _, run := range runs {
		if run.ParentID == result.RunID {
			actual[run.ID] = run
		}
	}
	executionIssue := false
	reported := map[string]bool{}
	workflows := map[string]bool{}
	for _, route := range output.Routes {
		if route.Target != "WORKER" && route.Target != "LEARNER" {
			fail(f.Fail("INVALID_FILTER_OUTPUT", "invalid route target"))
			return
		}

		if route.Target == "LEARNER" {
			if route.Execution == nil {
				executionIssue = true
				continue
			}
			run, ok := actual[route.Execution.RunID]
			reported[route.Execution.RunID] = true
			if !ok {
				fail(f.Fail("INVALID_FILTER_EXECUTION", "learner route does not match an actual child run"))
				return
			}
			target, lookupErr := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: b.OwnerID, AgentID: run.AgentID})
			if lookupErr != nil || target.Type != "learner" {
				fail(f.Fail("INVALID_FILTER_EXECUTION", "learner route references a different role"))
				return
			}
			var outcome struct {
				Status     string `json:"status"`
				Verified   bool   `json:"verified"`
				WorkflowID string `json:"workflow_id"`
			}
			if run.State != "succeeded" || json.Unmarshal([]byte(run.Output), &outcome) != nil || (outcome.Status != "APPLIED" && outcome.Status != "NO_CHANGE") || (outcome.Status == "APPLIED" && !outcome.Verified) {
				executionIssue = true
			}
			if route.WorkflowID != "" && route.WorkflowID != outcome.WorkflowID {
				fail(f.Fail("INVALID_FILTER_EXECUTION", "learner workflow ID differs from actual output"))
				return
			}
		}
		if route.Target == "WORKER" {
			if workflows[route.WorkflowID] {
				fail(f.Fail("INVALID_FILTER_OUTPUT", "duplicate workflow route"))
				return
			}
			workflows[route.WorkflowID] = true
			if route.Execution == nil {
				executionIssue = true
			} else {
				run, ok := actual[route.Execution.RunID]
				reported[route.Execution.RunID] = true
				if !ok || run.WorkflowID != route.WorkflowID {
					fail(f.Fail("INVALID_FILTER_EXECUTION", "route does not match an actual child run"))
					return
				}
				if run.State != "succeeded" || run.BusinessStatus != "COMPLETED" {
					executionIssue = true
				}
			}

			w, e := s.Agents.Registry.Repository.GetWorkflow(ctx, b.OwnerID, route.WorkflowID)
			if e != nil || w.Status != "active" {
				fail(f.Fail("INVALID_FILTER_OUTPUT", "route workflow not available"))
				return
			}
		}
	}
	b.Filter = json.RawMessage(result.Output)
	for id := range actual {
		if !reported[id] {
			executionIssue = true
		}
	}
	if executionIssue && output.Status != "PARTIAL" {
		finish("partial", "CHILD_EXECUTION_INCOMPLETE")
		return
	}
	switch output.Status {
	case "PARTIAL", "NEEDS_INFO", "NO_MATCH":
		finish("partial", output.Status)
	case "ERROR":
		finish("failed", "FILTER_ERROR")
	case "IGNORED":
		finish("ignored", "")
	default:
		finish("succeeded", "")
	}
}

// filterEvidence keeps change identity separate from archive and native IDs.
// Platform events carry platform_records.id; DWS events retain native message
// IDs and identify their dataset, field and conversation explicitly.
type filterEvidence struct {
	ChangeID     string `json:"change_id"`
	RecordID     string `json:"record_id"`
	ExternalID   string `json:"external_id"`
	ConnectionID string `json:"connection_id"`
	Dataset      string `json:"dataset,omitempty"`
	IDField      string `json:"id_field,omitempty"`
	GroupID      string `json:"group_id,omitempty"`
}

// DWS events historically store the native message ID in RecordID. Preserve
// that namespace and resolve it within its connection and conversation.
func dwsMessageGroup(change model.ControlChange) string {
	for _, raw := range []json.RawMessage{change.After, change.Before} {
		var identity struct {
			Group string `json:"conversationId"`
		}
		if json.Unmarshal(raw, &identity) == nil && identity.Group != "" {
			return identity.Group
		}
	}
	return ""
}

type filterChange struct {
	model.ControlChange
	ExternalID string `json:"external_id"`
}

func filterInput(b model.ControlBatch, selected []model.ControlChange) (json.RawMessage, error) {
	changes := make([]filterChange, 0, len(selected))
	links := make([]filterEvidence, 0, len(selected))
	changeIDs, recordIDs, externalIDs, connectionIDs := []string{}, []string{}, []string{}, []string{}
	for _, change := range selected {
		change.Before = cleanEvidenceTime(change.Before)
		change.After = cleanEvidenceTime(change.After)
		var externalID string
		// Deletions may only carry the old observation. Do not invent an external ID.
		for _, snapshot := range []json.RawMessage{change.After, change.Before} {
			var identity struct {
				ExternalID string `json:"external_id"`
			}
			if json.Unmarshal(snapshot, &identity) == nil && identity.ExternalID != "" {
				externalID = identity.ExternalID
				break
			}
		}
		link := filterEvidence{ChangeID: change.ID, RecordID: change.RecordID, ExternalID: externalID, ConnectionID: change.ConnectionID}
		if b.Source == "dws" && change.Resource == "message" {
			externalID = change.RecordID
			link.ExternalID = externalID
			link.Dataset, link.IDField, link.GroupID = "dws_messages", "external_id", dwsMessageGroup(change)
		}
		changes = append(changes, filterChange{ControlChange: change, ExternalID: externalID})
		links = append(links, link)
		changeIDs = append(changeIDs, change.ID)
		recordIDs = append(recordIDs, change.RecordID)
		externalIDs = append(externalIDs, externalID)
		connectionIDs = append(connectionIDs, change.ConnectionID)
	}
	input := map[string]any{
		"event_id": b.ID, "input_type": "EXTERNAL_EVENT", "content": changes,
		"source": map[string]any{"platform": b.Source},
		// Retain the existing string references while explicitly naming their namespace.
		"evidence_refs": changeIDs, "evidence_ref_kind": "change_id", "evidence_links": links,
		"change_ids": changeIDs, "record_ids": recordIDs, "external_ids": externalIDs, "connection_ids": connectionIDs,
		"evidence_query": map[string]string{"dataset": "platform_records", "id_field": "id", "id_value_from": "evidence_links[].record_id", "operator": "eq"},
		"context":        map[string]any{"refresh_id": b.RoundID, "trigger": b.Trigger, "time_semantics": "occurred_at是采集变化观察时间，不是源发布时间；源snapshot的零值observed_at已省略，不代表平台时间异常"},
	}
	if b.Source == "dws" {
		// Attachment events carry message_id:attachment_id and have no single
		// message query. Their evidence links must not claim platform-record IDs.
		delete(input, "evidence_query")
		messagesOnly := len(links) > 0
		for _, link := range links {
			messagesOnly = messagesOnly && link.Dataset == "dws_messages" && link.GroupID != ""
		}
		if messagesOnly {
			input["evidence_query"] = map[string]string{"dataset": "dws_messages", "id_field": "external_id", "id_value_from": "evidence_links[].external_id", "connection_id_from": "evidence_links[].connection_id", "group_id_from": "evidence_links[].group_id", "operator": "eq"}
		}
	}
	if courseBatch(b) {
		input["batch_scope"] = b.Scope
	}
	if len(selected) > 0 {
		input["occurred_at"] = selected[0].OccurredAt
	}
	return json.Marshal(input)
}

type triggerDecision struct {
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
	IDs      []string `json:"relevant_change_ids"`
}

func validateTrigger(raw []byte, changes []model.ControlChange) (out triggerDecision, err error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		return out, f.Fail("INVALID_TRIGGER_OUTPUT", "invalid trigger JSON")
	}
	if out.Decision != "TRIGGER" && out.Decision != "UNCERTAIN" && out.Decision != "IGNORE" {
		return out, f.Fail("INVALID_TRIGGER_OUTPUT", "invalid decision")
	}
	if out.Reason == "" || out.IDs == nil || out.Decision == "IGNORE" && len(out.IDs) != 0 || out.Decision != "IGNORE" && len(out.IDs) == 0 {
		return out, f.Fail("INVALID_TRIGGER_OUTPUT", "invalid relevant change list")
	}
	allowed := map[string]bool{}
	for _, v := range changes {
		allowed[v.ID] = true
	}
	selected := map[string]bool{}
	for _, id := range out.IDs {
		if !allowed[id] || selected[id] {
			return out, f.Fail("INVALID_TRIGGER_OUTPUT", "unknown or duplicate evidence ID")
		}
		selected[id] = true
	}
	out.IDs = []string{}
	for _, change := range changes {
		if selected[change.ID] {
			out.IDs = append(out.IDs, change.ID)
			delete(selected, change.ID)
		}
	}
	return out, nil
}
func pick(changes []model.ControlChange, ids []string) []model.ControlChange {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	out := []model.ControlChange{}
	for _, v := range changes {
		if set[v.ID] {
			out = append(out, v)
		}
	}
	return out
}

func (s *Service) StartRefresh(ctx context.Context) (string, error) {
	return s.Store.StartRound(ctx, s.instance)
}
