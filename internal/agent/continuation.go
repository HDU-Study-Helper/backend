package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	f "academic/agent/framework"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
)

type archiveMetaKey struct{}
type archiveMeta struct {
	target    string
	mu        sync.Mutex
	questions []c.Question
}

func (m *archiveMeta) add(qs ...c.Question) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions = append(m.questions, qs...)
}
func (m *archiveMeta) pending() []c.Question {
	m.mu.Lock()
	defer m.mu.Unlock()
	qs := append([]c.Question(nil), m.questions...)
	sort.Slice(qs, func(i, j int) bool { return qs[i].ID < qs[j].ID })
	return qs
}

type restoreKey struct{}
type restore struct {
	archive c.Archive
	parent  string
}

func handled(role string) bool {
	return role == "worker" || role == "learner" || role == "filter" || role == "chat"
}
func (s *Service) continuationStore() (c.Store, error) {
	store, ok := s.Registry.Repository.(c.Store)
	if !ok {
		return nil, f.Fail("NOT_IMPLEMENTED", "durable continuation store is unavailable")
	}
	return store, nil
}
func (s *Service) runWithContinuation(ctx context.Context, owner string, req f.RunRequest, a f.AgentInstance) (f.RunResult, error) {
	src, bound := c.Current(ctx)
	if bound && !src.Valid(owner) {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "invalid continuation source")
	}
	if !handled(a.Type) {
		return s.runRole(ctx, owner, req, a)
	}
	store, storeErr := s.continuationStore()
	if !bound && storeErr == nil && (a.Type == "filter" || a.Type == "worker" || a.Type == "learner") {
		ctx = c.WithSource(ctx, owner, c.ChA, f.NewID())
		src, bound = c.Current(ctx)
	}
	if !bound && a.Type == "chat" {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "Chat requires an authenticated conversation")
	}
	if !bound {
		return s.runRole(ctx, owner, req, a)
	}
	if storeErr != nil {
		return f.RunResult{}, storeErr
	}
	if a.Type == "chat" && src.Channel != c.ChB {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "Chat requires a chat source")
	}
	meta := new(archiveMeta)
	up, _ := ctx.Value(archiveMetaKey{}).(*archiveMeta)
	ctx = context.WithValue(ctx, archiveMetaKey{}, meta)
	var prior *f.Transcript
	var parent string
	if rr, ok := ctx.Value(restoreKey{}).(restore); ok {
		prior = &rr.archive.Transcript
		parent = rr.parent
		ctx = context.WithValue(ctx, restoreKey{}, struct{}{})
	}
	archive := c.Archive{Source: src, Role: a.Type}
	archive.Control, _ = c.Control(ctx, owner)
	if auth, ok := ctx.Value(learnerStageKey{}).(learnerAuthority); ok && a.Type == "learner" {
		archive.Path, archive.Target = auth.Path, auth.Target
	}
	_, archive.FirstAsOf, _ = model.FirstReadWindow(ctx, owner)
	if src.Channel == c.ChB && a.Type == "worker" {
		ctx = f.WithRoleContext(ctx, owner, a.ID, "This task came through Chat (ChB). Include a concise natural-language `text` field for Chat alongside your structured result. Never disclose private tool history.")
	}
	var q *c.Question
	atomicSaved := false
	ctx = f.WithTranscript(ctx, owner, a.ID, prior, func(t f.Transcript) {
		if prior != nil {
			t.Parent = parent
		}
		archive.Transcript = t
		if len(meta.pending()) == 0 {
			x, _ := readQuestion(a.Type, t.Messages[len(t.Messages)-1].Content)
			if x != nil {
				q = &c.Question{Interaction: *x, ID: f.NewID(), Text: x.Question, Role: a.Type, RunID: t.Run, Channel: src.Channel, Key: src.Key}
			}
		}
	})
	if a.Type == "learner" {
		ctx = c.WithCommitBinding(ctx, func(target string, receipt []byte) (c.Archive, *c.Question) {
			archive.Transcript.Messages = append(archive.Transcript.Messages, f.Message{Role: "user", Content: "Host staging commit receipt (completed, do not repeat): " + string(receipt)})
			if q != nil {
				atomicSaved = true
				archive.Path = "REVISE"
				archive.Target = target
			}
			return archive, q
		})
	}
	res, err := s.runRole(ctx, owner, req, a)
	if err != nil {
		return res, err
	}
	if archive.Transcript.Run == "" {
		return res, f.Fail("INTERNAL_ERROR", "logical transcript was not captured")
	}
	// A resumed parent is a fresh run; its logical parent remains the one that
	// actually handled the archived chain, never one named in model output.
	if prior != nil {
		archive.Transcript.Parent = parent
	}
	if meta.target != "" {
		archive.Path = "REVISE"
		archive.Target = meta.target
	}
	if !atomicSaved {
		if err = store.SaveContinuation(ctx, archive, q); err != nil {
			return res, err
		}
	}
	qs := meta.pending()
	if q != nil {
		qs = append(qs, *q)
	}
	if len(qs) > 0 {
		// Parent outputs cannot forge or replace the child's durable question id.
		// The owner-scoped pending list preserves every parallel question.
		q = &qs[0]
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(res.Output), &obj) == nil {
			obj["interaction"] = mustStageJSON(map[string]any{"type": q.Type, "question": q.Question, "options": q.Options, "question_id": q.ID})
			res.Output = string(mustStageJSON(obj))
		}
	}
	if up != nil {
		up.add(qs...)
	}
	return res, nil
}

// Only display content is accepted here; IDs, source, ownership, and parentage
// are deliberately absent from the input shape. Legacy Learner questions[] are
// normalized at this boundary, not treated as a second pending interaction.
func readQuestion(role, output string) (*c.Interaction, error) {
	if role != "worker" && role != "learner" && role != "chat" {
		return nil, nil
	}
	var v struct {
		Interaction *c.Interaction    `json:"interaction"`
		Questions   []json.RawMessage `json:"questions"`
		Status      string            `json:"status"`
	}
	if e := json.Unmarshal([]byte(output), &v); e != nil {
		return nil, e
	}
	x := v.Interaction
	if role == "learner" {
		if x != nil && len(v.Questions) > 0 {
			return nil, errors.New("use a single interaction; leave legacy questions empty")
		}
		if x == nil && len(v.Questions) > 0 {
			var qs []string
			for _, q := range v.Questions {
				var text string
				if json.Unmarshal(q, &text) != nil || strings.TrimSpace(text) == "" {
					return nil, errors.New("questions must contain nonempty strings")
				}
				qs = append(qs, text)
			}
			x = &c.Interaction{Type: "TEXT_INPUT", Question: strings.Join(qs, "\n")}
		}
		if x != nil && v.Status != "READY" && v.Status != "NO_CHANGE" && v.Status != "NEEDS_INFO" {
			return nil, errors.New("only a successful or waiting result can request a reply")
		}
	}
	if x == nil {
		return nil, nil
	}
	x.Question = strings.TrimSpace(x.Question)
	if x.Options == nil {
		x.Options = []c.Option{}
	}
	if len(x.Question) == 0 || len(x.Question) > 4096 || len(x.Options) > 40 {
		return nil, errors.New("interaction requires a short nonempty question and at most 40 options")
	}
	switch x.Type {
	case "CHOICE":
		if len(x.Options) == 0 {
			return nil, errors.New("CHOICE requires options")
		}
	case "CONFIRM", "TEXT_INPUT":
	default:
		return nil, errors.New("invalid interaction type")
	}
	seen := map[string]bool{}
	for _, op := range x.Options {
		if strings.TrimSpace(op.ID) == "" || len(op.ID) > 128 || seen[op.ID] || strings.TrimSpace(op.Label) == "" || len(op.Label) > 512 {
			return nil, errors.New("invalid or duplicate interaction option")
		}
		seen[op.ID] = true
	}
	return x, nil
}
func validateWorkerFinal(ctx context.Context, in f.OutputValidationInput) (*f.OutputValidationFailure, error) {
	if bad, e := validateJSONObjectFinal(ctx, in); bad != nil || e != nil {
		return bad, e
	}
	if _, e := readQuestion("worker", in.Output); e != nil {
		return &f.OutputValidationFailure{Kind: f.OutputFormat, Code: "INVALID_WORKER_RESULT", Feedback: e.Error()}, nil
	}
	if src, ok := c.Current(ctx); ok && src.Channel == c.ChB {
		var v struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(in.Output), &v) != nil || strings.TrimSpace(v.Text) == "" {
			return &f.OutputValidationFailure{Kind: f.OutputFormat, Code: "INVALID_WORKER_RESULT", Feedback: "Include a natural-language text field for Chat in addition to structured results."}, nil
		}
	}
	return nil, nil
}
func (s *Service) PendingQuestions(ctx context.Context, owner, channel, key string) ([]c.Question, error) {
	src := c.Source{Owner: owner, Channel: channel, Key: key}
	if !src.Valid(owner) && !(owner != "" && channel == c.ChA && key == "") {
		return nil, f.Fail("INVALID_ARGUMENT", "invalid pending question scope")
	}
	st, err := s.continuationStore()
	if err != nil {
		return nil, err
	}
	return st.PendingContinuations(ctx, src)
}
func (s *Service) CancelPendingChB(ctx context.Context, owner, key string) error {
	src := c.Source{Owner: owner, Channel: c.ChB, Key: key}
	if !src.Valid(owner) {
		return f.Fail("INVALID_ARGUMENT", "invalid conversation")
	}
	st, err := s.continuationStore()
	if err != nil {
		return err
	}
	return st.CancelContinuations(ctx, src)
}
func (s *Service) restoreRun(ctx context.Context, owner string, a c.Archive, parent string, input json.RawMessage) (f.RunResult, error) {
	if !a.Source.Valid(owner) || a.Transcript.Owner != owner || !handled(a.Role) {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "archive owner or role mismatch")
	}
	current, err := s.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: owner, AgentID: a.Transcript.Agent})
	if err != nil {
		return f.RunResult{}, err
	}
	if current.Type != a.Role {
		return f.RunResult{}, f.Fail("CONFLICT", "archived agent role changed")
	}
	ctx = context.WithValue(ctx, restoreKey{}, restore{a, parent})
	if a.Transcript.Workflow != "" {
		ctx = f.WorkflowContext(ctx, owner, a.Transcript.Workflow)
	}
	if a.Role == "learner" {
		ctx = WithLearnerStage(ctx, owner, a.Path, a.Target)
	}
	if !a.FirstAsOf.IsZero() {
		ctx = model.WithFirstReadWindow(ctx, owner, a.FirstAsOf)
	}
	return s.Run(ctx, owner, f.RunRequest{AgentID: a.Transcript.Agent, Trigger: f.SystemEvent, Input: input})
}
func (s *Service) Answer(ctx context.Context, owner, id, answer string) (out f.RunResult, err error) {
	src, ok := c.Current(ctx)
	if !ok || !src.Valid(owner) || id == "" || strings.TrimSpace(answer) == "" || len(answer) > 16384 {
		return out, f.Fail("INVALID_ARGUMENT", "answer requires owner, source, question and nonempty text")
	}
	st, err := s.continuationStore()
	if err != nil {
		return out, err
	}
	q, done, err := st.ClaimContinuation(ctx, src, id, answer)
	if err != nil {
		return out, err
	}
	if done != nil {
		if done.Error != nil {
			return *done, done.Error
		}
		return *done, nil
	}
	var resumedRuns []string
	defer func() {
		if link, ok := c.Control(ctx, owner); ok && s.ContinuationFinished != nil && err == nil {
			err = s.ContinuationFinished(ctx, link, resumedRuns, out)
		}
		out.Error = f.SafeError(err)
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := st.FinishContinuation(finish, src, id, out); e != nil && err == nil {
			err = e
		}
	}()
	ctx = c.WithAnswerRef(ctx, owner, q.ID)
	ref := c.UserMessageRef(ctx, owner)
	input := mustStageJSON(map[string]any{"interaction": q.Interaction, "answer": answer, "user_message_ref": ref})
	run := q.RunID
	seen := map[string]bool{}
	for run != "" {
		if seen[run] {
			return out, f.Fail("CONFLICT", "invalid cyclic archive")
		}
		seen[run] = true
		a, e := st.LoadContinuation(ctx, owner, run)
		if e != nil {
			return out, e
		}
		if a.Source != src {
			return out, f.Fail("PERMISSION_DENIED", "historical chain source changed")
		}
		if a.Control.Owner == owner && a.Control.Batch != "" {
			ctx = c.WithControl(ctx, a.Control)
		}
		marker := new(archiveMeta)
		out, e = s.restoreRun(context.WithValue(ctx, archiveMetaKey{}, marker), owner, a, a.Transcript.Parent, input)
		if e != nil {
			return out, e
		}
		resumedRuns = append(resumedRuns, out.RunID)
		if len(marker.pending()) > 0 {
			return out, nil
		}
		run = a.Transcript.Parent
		if run != "" {
			r, e := s.Registry.Repository.GetRun(ctx, owner, run)
			if e != nil {
				return out, e
			}
			ag, e := s.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: owner, AgentID: r.AgentID})
			if e != nil {
				return out, e
			}
			if !handled(ag.Type) {
				break
			}
			input = mustStageJSON(map[string]string{"resumed_child_role": a.Role, "child_result": handoff(ag.Type, out.Output), "child_run_id": out.RunID, "user_message_ref": ref})
		}
	}
	return out, nil
}
func handoff(parentRole, raw string) string {
	// Worker/Filter retain the structured child result (including the current
	// staging receipt) just like their original dispatch. Chat gets the child's
	// user-facing text and artifact references, not its transcript or private data.
	if parentRole != "chat" {
		return raw
	}
	var obj struct {
		Text       string `json:"text,omitempty"`
		Summary    string `json:"summary,omitempty"`
		Status     string `json:"status,omitempty"`
		WorkflowID string `json:"workflow_id,omitempty"`
		Version    int    `json:"workflow_version,omitempty"`
		Verified   bool   `json:"verified,omitempty"`
		Artifacts  []struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description,omitempty"`
			Ref         string `json:"entity_ref,omitempty"`
		} `json:"artifacts,omitempty"`
	}
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return ""
	}
	return string(mustStageJSON(obj))
}
func (s *Service) RunScheduledWorker(ctx context.Context, owner, origin string, input json.RawMessage) (f.RunResult, error) {
	st, err := s.continuationStore()
	if err != nil {
		return f.RunResult{}, err
	}
	a, err := st.LoadContinuation(ctx, owner, origin)
	if err != nil {
		return f.RunResult{}, err
	}
	if a.Role != "worker" || a.Transcript.Workflow == "" {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "schedule requires an archived bound Worker")
	}
	ctx = c.WithSource(ctx, owner, c.ChA, "schedule:"+f.NewID())
	return s.restoreRun(ctx, owner, a, "", input)
}
