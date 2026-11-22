package learnerstage

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type Session struct {
	f.Repository
	Store                               Store
	ID, Path, Owner, Agent, FixedTarget string
	Base                                *Snapshot
	View                                Snapshot
	Revision                            int
	Deleted                             bool
	FirstRun                            bool
	ToolFailures                        map[string]*f.Error
	PendingRun                          *f.RunRecord
}

func New(repo f.Repository, store Store, owner, agent, path, target string) *Session {
	return &Session{Repository: repo, Store: store, ID: f.NewID(), Path: path, Owner: owner, Agent: agent, FixedTarget: target, View: Snapshot{Memories: []f.Memory{}, Relations: []f.MemoryRelation{}}}
}

// SaveRun persists the running audit immediately, but transfers terminal audit
// ownership to the host. The model runtime's completion is provisional until
// the host has validated/committed staging; production SaveRun permits exactly
// one running-to-terminal transition.
func (s *Session) SaveRun(ctx context.Context, record f.RunRecord) error {
	if record.OwnerID != s.Owner || record.AgentID != s.Agent {
		return f.Fail("PERMISSION_DENIED", "staging run identity mismatch")
	}
	if record.Status == "running" {
		return s.Repository.SaveRun(ctx, record)
	}
	copy := Clone(record)
	s.PendingRun = &copy
	return nil
}

func (s *Session) check(scope f.Scope) error {
	if scope.OwnerID != s.Owner || scope.AgentID != s.Agent {
		return f.Fail("PERMISSION_DENIED", "staging identity mismatch")
	}
	return nil
}
func (s *Session) Select(ctx context.Context, id string) error {
	// A newly staged Workflow already belongs to this session, even when
	// no persisted target (FixedTarget) existed when the run started.
	if s.View.Workflow.ID != "" {
		if s.View.Workflow.ID != id {
			return f.Fail("PERMISSION_DENIED", "one staging session has one Workflow")
		}
		return nil
	}
	if s.FixedTarget != "" && id != s.FixedTarget {
		return f.Fail("PERMISSION_DENIED", "cannot select a Workflow outside the authorized target")
	}
	snap, err := s.Store.LearnerSnapshot(ctx, f.Scope{OwnerID: s.Owner, AgentID: s.Agent}, id)
	if err != nil {
		return err
	}
	s.Base = &snap
	s.View = Clone(snap)
	s.Revision++
	return nil
}
func (s *Session) State() any {
	blockers := []string{}
	if s.View.Workflow.ID == "" {
		blockers = append(blockers, "select or stage-create a target Workflow")
	}
	if s.Path == "INITIALIZE" {
		if s.View.Workflow.Status != "active" {
			blockers = append(blockers, "initialization target is not active; finish its definition and explicitly stage status=active before completion")
		}
		if len(s.View.Memories) == 0 {
			blockers = append(blockers, "initialization has no sourced target private knowledge")
		}
	}
	return map[string]any{"protocol": Protocol, "session_id": s.ID, "path": s.Path, "workflow_id": s.View.Workflow.ID, "workflow_status": s.View.Workflow.Status, "memory_count": len(s.View.Memories), "base_version": s.baseVersion(), "staging_revision": s.Revision, "staged": true, "committed": false, "completion_blockers": blockers}
}
func (s *Session) baseVersion() any {
	if s.Base == nil {
		return nil
	}
	return s.Base.Workflow.Version
}

// LockWorkflow preserves the host's optional cross-process serialization.
func (s *Session) LockWorkflow(ctx context.Context, scope f.Scope) (func(), error) {
	if locker, ok := s.Repository.(f.WorkflowLocker); ok {
		return locker.LockWorkflow(ctx, scope)
	}
	return func() {}, nil
}

func (s *Session) GetAgent(ctx context.Context, scope f.Scope) (f.AgentInstance, error) {
	a, err := s.Repository.GetAgent(ctx, scope)
	if err == nil && a.ID == s.Agent {
		a.EnabledTools = append(a.EnabledTools, "learner_stage_select", "learner_stage_state")
		a.EnabledTools = without(a.EnabledTools, "agent_dispatch", "notification_send", "schedule_create", "schedule_update", "schedule_delete")
	}
	return a, err
}
func without(in []string, names ...string) []string {
	out := []string{}
	for _, v := range in {
		skip := false
		for _, n := range names {
			skip = skip || v == n
		}
		if !skip {
			out = append(out, v)
		}
	}
	return out
}
func (s *Session) GetWorkflow(ctx context.Context, owner, id string) (f.Workflow, error) {
	if owner != s.Owner {
		return f.Workflow{}, f.Fail("PERMISSION_DENIED", "foreign owner")
	}
	if s.View.Workflow.ID == id {
		if s.Deleted {
			return f.Workflow{}, f.Fail("RESOURCE_NOT_FOUND", "Workflow deleted in staging")
		}
		return Clone(s.View.Workflow), nil
	}
	return s.Repository.GetWorkflow(ctx, owner, id)
}
func (s *Session) SaveWorkflow(ctx context.Context, scope f.Scope, w f.Workflow, create bool) (f.Workflow, error) {
	if err := s.check(scope); err != nil {
		return f.Workflow{}, err
	}
	if create {
		if s.Path != "INITIALIZE" || s.View.Workflow.ID != "" {
			return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "select one target; cannot create another")
		}
		w.ID = f.NewID()
		w.OwnerID = s.Owner
		w.CreatedBy = s.Agent
		w.Version = 1
		w.CreatedAt = time.Now().UTC()
	} else {
		if s.View.Workflow.ID == "" {
			if err := s.Select(ctx, w.ID); err != nil {
				return f.Workflow{}, err
			}
		}
		old := s.View.Workflow
		if w.ID != old.ID || w.Version != old.Version || w.ParentID != old.ParentID {
			return f.Workflow{}, f.Fail("CONFLICT", "target/version/parent differs from staged Workflow")
		}
		w.OwnerID = s.Owner
		w.CreatedBy = old.CreatedBy
		w.CreatedAt = old.CreatedAt
	}
	if strings.TrimSpace(w.Name) == "" || !json.Valid(w.Definition) || len(w.Definition) > 1<<20 {
		return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "name and definition are required")
	}
	var d map[string]any
	if json.Unmarshal(w.Definition, &d) != nil || d == nil {
		return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "definition must be an object")
	}
	if w.Status == "" {
		w.Status = "draft"
	}
	if w.Status != "draft" && w.Status != "active" && w.Status != "archived" {
		return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "invalid status")
	}
	if w.Status == "active" && !model.ValidWorkflowEntity(w.Definition) {
		return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "active Workflow requires a specific entity")
	}
	if create && w.Status == "archived" {
		return f.Workflow{}, f.Fail("INVALID_ARGUMENT", "new Workflow cannot be archived")
	}
	w.UpdatedAt = time.Now().UTC()
	s.View.Workflow = Clone(w)
	s.Revision++
	return Clone(w), nil
}
func (s *Session) DeleteWorkflow(ctx context.Context, owner, id string) error {
	if owner != s.Owner || id != s.View.Workflow.ID || s.Base == nil {
		return f.Fail("PERMISSION_DENIED", "select an existing target before deleting")
	}
	s.Deleted = true
	s.Revision++
	return nil
}
func (s *Session) ListMemory(ctx context.Context, scope f.Scope, q string, limit int) ([]f.Memory, error) {
	return s.SearchMemory(ctx, scope, f.MemorySearch{Query: q, Limit: limit})
}
func (s *Session) SearchMemory(ctx context.Context, scope f.Scope, q f.MemorySearch) ([]f.Memory, error) {
	if err := s.check(scope); err != nil {
		return nil, err
	}
	if q.WorkflowID != "" && q.WorkflowID != s.View.Workflow.ID {
		return nil, f.Fail("PERMISSION_DENIED", "foreign private scope")
	}
	if q.AgentID != "" && q.AgentID != s.Agent {
		return nil, f.Fail("PERMISSION_DENIED", "foreign Agent PRI")
	}
	// Own Agent/PUB remain readable. No model-selected Workflow scope is sent to base.
	plain := q
	plain.WorkflowID = ""
	base, err := s.Repository.SearchMemory(ctx, f.Scope{OwnerID: s.Owner, AgentID: s.Agent}, plain)
	if err != nil {
		return nil, err
	}
	out := []f.Memory{}
	all := append(base, s.View.Memories...)
	now := time.Now()
	for _, m := range all {
		if q.WorkflowID != "" && m.WorkflowID != q.WorkflowID || q.AgentID != "" && m.AgentID != q.AgentID || q.Visibility != "" && m.Visibility != q.Visibility || q.MemoryType != "" && m.MemoryType != q.MemoryType || q.Level != "" && m.Level != q.Level || q.ParentID != "" && m.ParentID != q.ParentID || q.IsPersonal != nil && m.IsPersonal != *q.IsPersonal {
			continue
		}
		status := q.Status
		if status == "" {
			status = "active"
		}
		if status != "all" && m.Status != status {
			continue
		}
		valid := q.Validity
		if valid == "" {
			valid = "current"
		}
		future := m.ValidFrom != nil && m.ValidFrom.After(now)
		expired := m.ValidUntil != nil && !m.ValidUntil.After(now)
		if valid == "current" && (future || expired) || valid == "future" && !future || valid == "expired" && !expired {
			continue
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(m.Title+" "+m.Content+" "+m.Category+" "+string(m.StructuredContent)), strings.ToLower(q.Query)) {
			continue
		}
		out = append(out, Clone(m))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Session) GetMemory(ctx context.Context, scope f.Scope, id string) (f.Memory, error) {
	if err := s.check(scope); err != nil {
		return f.Memory{}, err
	}
	for _, m := range s.View.Memories {
		if m.ID == id {
			return Clone(m), nil
		}
	}
	if s.Base != nil {
		for _, m := range s.Base.Memories {
			if m.ID == id {
				return f.Memory{}, f.Fail("RESOURCE_NOT_FOUND", "memory deleted in staging")
			}
		}
	}
	return s.Repository.GetMemory(ctx, f.Scope{OwnerID: s.Owner, AgentID: s.Agent}, id)
}
func (s *Session) SaveMemory(_ context.Context, scope f.Scope, m f.Memory, create bool) (f.Memory, error) {
	if err := s.check(scope); err != nil {
		return f.Memory{}, err
	}
	target := s.View.Workflow.ID
	if target == "" || s.Deleted {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "select or stage-create a Workflow first")
	}
	if m.OwnerID != "" && m.OwnerID != s.Owner || m.AgentID != "" || m.WorkflowID != "" && m.WorkflowID != target || m.Visibility != "" && m.Visibility != "PRI" {
		return f.Memory{}, f.Fail("PERMISSION_DENIED", "staged writes are target Workflow PRI only")
	}
	idx := -1
	for i, v := range s.View.Memories {
		if v.ID == m.ID {
			idx = i
		}
	}
	if !create && idx < 0 {
		return f.Memory{}, f.Fail("PERMISSION_DENIED", "memory is outside selected target")
	}
	if create {
		m.ID = f.NewID()
		m.CreatedAt = time.Now().UTC()
	}
	m.OwnerID = s.Owner
	m.WorkflowID = target
	m.AgentID = ""
	m.Visibility = "PRI"
	if m.MemoryType == "" {
		m.MemoryType = "BASE"
	}
	if m.Level == "" {
		m.Level = "L1"
	}
	if m.Status == "" {
		m.Status = "active"
	}
	if strings.TrimSpace(m.Title) == "" || strings.TrimSpace(m.Content) == "" || (m.MemoryType != "BASE" && m.MemoryType != "TEMP") || (m.Level != "L0" && m.Level != "L1" && m.Level != "L2" && m.Level != "L3") || (m.Status != "active" && m.Status != "archived" && m.Status != "invalid") || m.ValidFrom != nil && m.ValidUntil != nil && !m.ValidUntil.After(*m.ValidFrom) {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "invalid staged memory fields or validity")
	}
	var refs []json.RawMessage
	if json.Unmarshal(m.SourceRefs, &refs) != nil || len(refs) == 0 {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "staged memory requires source_refs")
	}
	if len(m.StructuredContent) > 0 {
		var obj map[string]any
		if json.Unmarshal(m.StructuredContent, &obj) != nil || obj == nil {
			return f.Memory{}, f.Fail("INVALID_ARGUMENT", "structured_content must be an object")
		}
	}
	m.UpdatedAt = time.Now().UTC()
	if idx < 0 {
		s.View.Memories = append(s.View.Memories, Clone(m))
	} else {
		s.View.Memories[idx] = Clone(m)
	}
	s.Revision++
	return Clone(m), nil
}
func (s *Session) DeleteMemory(_ context.Context, scope f.Scope, id string) error {
	if err := s.check(scope); err != nil {
		return err
	}
	for i, m := range s.View.Memories {
		if m.ID == id {
			s.View.Memories = append(s.View.Memories[:i], s.View.Memories[i+1:]...)
			rels := []f.MemoryRelation{}
			for _, r := range s.View.Relations {
				if r.SourceID != id && r.TargetID != id {
					rels = append(rels, r)
				}
			}
			s.View.Relations = rels
			for j := range s.View.Memories {
				if s.View.Memories[j].ParentID == id {
					s.View.Memories[j].ParentID = ""
				}
			}
			s.Revision++
			return nil
		}
	}
	return f.Fail("PERMISSION_DENIED", "memory is outside target")
}
func (s *Session) CreateMemoryRelation(_ context.Context, scope f.Scope, r f.MemoryRelation) (f.MemoryRelation, error) {
	if err := s.check(scope); err != nil {
		return r, err
	}
	found := map[string]bool{}
	for _, m := range s.View.Memories {
		found[m.ID] = true
	}
	if !found[r.SourceID] || !found[r.TargetID] || r.SourceID == r.TargetID {
		return r, f.Fail("PERMISSION_DENIED", "relation endpoints must be selected Workflow memories")
	}
	for _, v := range s.View.Relations {
		if v.SourceID == r.SourceID && v.TargetID == r.TargetID && v.Type == r.Type {
			return r, f.Fail("INVALID_ARGUMENT", "relation already exists")
		}
	}
	r.ID = f.NewID()
	r.OwnerID = s.Owner
	r.CreatedBy = s.Agent
	r.CreatedAt = time.Now().UTC()
	s.View.Relations = append(s.View.Relations, r)
	s.Revision++
	return r, nil
}
func (s *Session) GetMemoryRelations(ctx context.Context, scope f.Scope, q f.MemoryRelationQuery) ([]f.MemoryRelation, error) {
	if err := s.check(scope); err != nil {
		return nil, err
	}
	target := false
	for _, m := range s.View.Memories {
		target = target || m.ID == q.MemoryID
	}
	if !target {
		return s.Repository.GetMemoryRelations(ctx, f.Scope{OwnerID: s.Owner, AgentID: s.Agent}, q)
	}
	out := []f.MemoryRelation{}
	for _, r := range s.View.Relations {
		if q.Type != "" && r.Type != q.Type {
			continue
		}
		if (q.Direction == "" || q.Direction == "both" || q.Direction == "outgoing") && r.SourceID == q.MemoryID || (q.Direction == "" || q.Direction == "both" || q.Direction == "incoming") && r.TargetID == q.MemoryID {
			out = append(out, r)
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return Clone(out), nil
}
