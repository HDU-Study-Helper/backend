// Package chatapi is the user-scoped App transport. No frontend rendering,
// device delivery, or caller-selected Agent/Workflow scopes are implemented here.
package chatapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/chatwire"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

type API struct {
	Agent   *agent.Service
	Store   model.ChatStore
	Actions model.ActionRepository
}

func (a API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := httpkit.CurrentUser(r.Context()); !ok {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Post("/threads", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		v, e := a.Store.NewChatThread(r.Context(), u.ID)
		respond(w, r, 201, v, e)
	})
	r.Get("/threads/{thread}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		id := chi.URLParam(r, "thread")
		turns, e := a.Store.ListChatTurns(r.Context(), u.ID, id, 100)
		if e != nil {
			respond(w, r, 200, nil, e)
			return
		}
		q, e := a.Agent.PendingQuestions(r.Context(), u.ID, c.ChB, id)
		respond(w, r, 200, map[string]any{"turns": turns, "pending_questions": q}, e)
	})
	r.Post("/threads/{thread}/messages", a.turn(false))
	r.Post("/threads/{thread}/answers", a.turn(true))
	r.Get("/silent-questions", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		q, e := a.Agent.PendingQuestions(r.Context(), u.ID, c.ChA, "")
		respond(w, r, 200, map[string]any{"items": q}, e)
	})
	r.Post("/silent-questions/{id}/answers", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		var in struct {
			Source string `json:"source_key"`
			Answer string `json:"answer"`
		}
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		ctx := c.WithSource(r.Context(), u.ID, c.ChA, in.Source)
		v, e := a.Agent.Answer(ctx, u.ID, chi.URLParam(r, "id"), in.Answer)
		if e != nil {
			respond(w, r, 200, nil, e)
			return
		} // Never return a private workflow transcript to the App.
		var summary struct {
			Summary string `json:"summary"`
			Status  string `json:"status"`
		}
		_ = json.Unmarshal([]byte(v.Output), &summary)
		respond(w, r, 200, map[string]any{"run_id": v.RunID, "result": summary}, nil)
	})
	for path, kind := range map[string]string{"/todos": "todo", "/notices": "notice"} {
		r.Get(path, func(w http.ResponseWriter, r *http.Request) {
			u, _ := httpkit.CurrentUser(r.Context())
			if a.Actions == nil {
				respond(w, r, 200, nil, model.ErrUnsupported)
				return
			}
			limit, offset := 100, 0
			var err error
			if raw := r.URL.Query().Get("limit"); raw != "" {
				limit, err = strconv.Atoi(raw)
			}
			if raw := r.URL.Query().Get("offset"); raw != "" && err == nil {
				offset, err = strconv.Atoi(raw)
			}
			if err != nil || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
				respond(w, r, 400, nil, model.ErrInvalidArgument)
				return
			}
			xs, e := a.Actions.ListActions(r.Context(), u.ID, kind, limit, offset)
			respond(w, r, 200, map[string]any{"items": xs, "offset": offset, "next_offset": offset + len(xs)}, e)
		})
	}
	return r
}
func (a API) chatAgent(ctx context.Context, owner string) (f.AgentInstance, error) {
	var selected f.AgentInstance
	for after := ""; ; {
		rows, e := a.Agent.Registry.Repository.ListAgents(ctx, owner, 100, after)
		if e != nil {
			return selected, e
		}
		for _, ag := range rows {
			if ag.Type == "chat" && ag.Status == "enabled" {
				if selected.ID != "" {
					return selected, model.ErrConflict
				}
				selected = ag
			}
		}
		if len(rows) < 100 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	if selected.ID == "" {
		return selected, model.ErrNotFound
	}
	return selected, nil
}
func (a API) turn(answer bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		thread := chi.URLParam(r, "thread")
		var in struct {
			Request  string `json:"request_id"`
			Message  string `json:"message"`
			Question string `json:"question_id,omitempty"`
		}
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		kind := "message"
		if answer {
			kind = "answer"
		}
		ag, e := a.chatAgent(r.Context(), u.ID)
		if e != nil {
			respond(w, r, 200, nil, e)
			return
		}
		t, created, e := a.Store.BeginChatTurn(r.Context(), u.ID, thread, in.Request, kind, in.Message, in.Question)
		if e != nil {
			respond(w, r, 200, nil, e)
			return
		}
		if !created {
			code := 200
			if t.State == "running" {
				code = 202
			}
			respond(w, r, code, t, nil)
			return
		}
		ctx, cancel := context.WithCancel(c.WithSource(r.Context(), u.ID, c.ChB, thread))
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			tick := time.NewTicker(20 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if a.Store.RenewChatTurn(ctx, u.ID, thread, t.ID) != nil {
						cancel()
						return
					}
				}
			}
		}()
		defer func() { cancel(); <-done }()
		var result f.RunResult
		if answer {
			result, e = a.Agent.Answer(ctx, u.ID, in.Question, in.Message)
		} else if e = a.Agent.CancelPendingChB(ctx, u.ID, thread); e == nil {
			var history []model.ChatTurn
			history, e = a.Store.ListChatTurns(ctx, u.ID, thread, 24)
			if e == nil {
				type entry struct {
					User  string `json:"user"`
					Reply string `json:"reply"`
				}
				past := []entry{}
				size := 0
				for i := len(history) - 1; i >= 0; i-- {
					h := history[i]
					if h.State != "completed" {
						continue
					}
					var old chatwire.Result
					_ = json.Unmarshal(h.Output, &old)
					if size+len(h.Message)+len(old.Text) > 24<<10 {
						break
					}
					past = append(past, entry{h.Message, old.Text})
					size += len(h.Message) + len(old.Text)
				}
				for i, j := 0, len(past)-1; i < j; i, j = i+1, j-1 {
					past[i], past[j] = past[j], past[i]
				}
				payload, _ := json.Marshal(map[string]any{"message": in.Message, "history": past, "user_message_ref": "chat_turn:" + t.ID})
				result, e = a.Agent.Run(ctx, u.ID, f.RunRequest{AgentID: ag.ID, Trigger: f.UserMessage, Input: payload})
			}
		}
		var raw json.RawMessage
		if e == nil {
			var questions []c.Question
			questions, e = a.Agent.PendingQuestions(ctx, u.ID, c.ChB, thread)
			if e == nil {
				v, parseErr := chatwire.Decode(result.Output)
				if parseErr != nil && !(answer && len(questions) > 0) {
					e = f.Fail("INVALID_CHAT_RESULT", "Chat did not return a valid conversation result")
				} else {
					if parseErr != nil {
						v = chatwire.Result{Text: questions[0].Text, Cards: []json.RawMessage{}}
					}
					if len(questions) > 0 {
						q := questions[0]
						if v.Interaction != nil {
							for _, candidate := range questions {
								if candidate.Text == v.Interaction.Question {
									q = candidate
									break
								}
							}
						}
						v.Interaction = &chatwire.Interaction{ID: q.ID, Type: q.Type, Question: q.Question}
						for _, option := range q.Options {
							v.Interaction.Options = append(v.Interaction.Options, chatwire.AnswerOption{ID: option.ID, Label: option.Label})
						}
					} else if v.Interaction != nil {
						e = f.Fail("INTERNAL_ERROR", "question was not persisted")
					}
					if e == nil {
						raw, _ = json.Marshal(v)
					}
				}
			}
		}
		code := ""
		if e != nil {
			code = f.SafeError(e).Code
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		finishErr := a.Store.FinishChatTurn(cleanup, u.ID, thread, t.ID, result.RunID, code, raw)
		if finishErr != nil {
			respond(w, r, 500, nil, finishErr)
			return
		}
		t.RunID, t.Output, t.Error = result.RunID, raw, code
		t.State = "completed"
		if e != nil {
			t.State = "failed"
		}
		respond(w, r, 200, t, e)
	}
}
func respond(w http.ResponseWriter, r *http.Request, status int, v any, e error) {
	if e == nil {
		httpkit.WriteJSON(w, status, v)
		return
	}
	code := f.SafeError(e).Code
	msg := "请求失败；已提交的操作不会回滚"
	switch {
	case errors.Is(e, model.ErrInvalidArgument) || code == "INVALID_ARGUMENT":
		status, code, msg = 400, "invalid_argument", "请求格式、回答或输入无效"
	case errors.Is(e, model.ErrNotFound) || code == "RESOURCE_NOT_FOUND":
		status, code, msg = 404, "not_found", "会话、问题或可用智能体不存在"
	case code == "RATE_LIMITED":
		status, msg = http.StatusTooManyRequests, "模型服务或智能体暂时达到请求限制，请稍后再试"
	case errors.Is(e, model.ErrConflict) || code == "CONFLICT":
		status, code, msg = 409, "conflict", "会话正在处理、请求已变化或问题已失效，请刷新"
	case code == "PERMISSION_DENIED":
		status, msg = 403, "无权处理该请求"
	case code == "MODEL_SERVICE_SELECTION_REQUIRED":
		status, msg = http.StatusUnprocessableEntity, "有多个可用模型服务，请在框架配置中选择默认模型服务"
	case code == "PROVIDER_ERROR":
		status, msg = 502, "模型服务不可用，请检查服务配置"
	default:
		status = 500
		if strings.TrimSpace(code) == "" {
			code = "internal_error"
		}
	}
	httpkit.Error(w, r, status, code, msg)
}
