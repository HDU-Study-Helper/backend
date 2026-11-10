package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/bridge"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

type Service struct {
	Repo     model.ScheduleRepository
	Fallback bridge.Port
}

func New(repo model.ScheduleRepository, fallback bridge.Port) *Service {
	return &Service{Repo: repo, Fallback: fallback}
}

type CreateInput struct {
	model.Semester
	model.ScheduleFields
}
type UpdateInput struct {
	ID string `json:"id"`
	model.ScheduleUpdate
}

func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return model.ErrInvalidArgument
	}
	if d.Decode(new(any)) != io.EOF {
		return model.ErrInvalidArgument
	}
	return nil
}
func (s *Service) Call(ctx context.Context, r bridge.Request) (bridge.Reply, error) {
	if r.Version != bridge.Version || r.OwnerID == "" || len(r.Payload) > 65536 || !json.Valid(r.Payload) {
		return bridge.Reply{}, bridge.ErrInvalid
	}
	if !strings.HasPrefix(r.Operation, "schedule.") {
		if s.Fallback != nil {
			return s.Fallback.Call(ctx, r)
		}
		return bridge.Reply{}, bridge.ErrUnsupported
	}
	var result any
	var err error
	switch r.Operation {
	case "schedule.create":
		var in CreateInput
		if err = decode(r.Payload, &in); err == nil {
			result, err = s.Repo.Create(ctx, r.OwnerID, model.Schedule{Semester: in.Semester, ScheduleFields: in.ScheduleFields, Source: "AGENT"})
		}
	case "schedule.query":
		var in model.ScheduleQuery
		if err = decode(r.Payload, &in); err == nil {
			result, err = s.Repo.List(ctx, r.OwnerID, in)
		}
	case "schedule.get", "schedule.delete":
		var in struct {
			ID string `json:"id"`
		}
		if err = decode(r.Payload, &in); err == nil {
			if r.Operation == "schedule.get" {
				result, err = s.Repo.Get(ctx, r.OwnerID, in.ID)
			} else {
				err = s.Repo.Delete(ctx, r.OwnerID, in.ID)
				result = map[string]bool{"deleted": err == nil}
			}
		}
	case "schedule.update":
		var in UpdateInput
		if err = decode(r.Payload, &in); err == nil {
			result, err = s.Repo.Update(ctx, r.OwnerID, in.ID, in.ScheduleUpdate)
		}
	default:
		return bridge.Reply{}, bridge.ErrUnsupported
	}
	if err != nil {
		return bridge.Reply{}, err
	}
	raw, err := json.Marshal(result)
	return bridge.Reply{Version: bridge.Version, Operation: r.Operation, Payload: raw}, err
}
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	code, status, message := "internal", 500, "日程操作失败"
	switch {
	case errors.Is(err, model.ErrInvalidArgument):
		code, status, message = "invalid_argument", 400, "请检查学期、教学周、起止时间和修改模式"
	case errors.Is(err, model.ErrNotFound):
		code, status, message = "not_found", 404, "日程不存在"
	case errors.Is(err, model.ErrConflict):
		code, status, message = "conflict", 409, "日程修改冲突或历史数量已达上限"
	case errors.Is(err, model.ErrUnauthenticated):
		code, status, message = "unauthenticated", 401, "请先登录"
	}
	httpkit.Error(w, r, status, code, message)
}
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := httpkit.CurrentUser(r.Context()); !ok {
				respondError(w, r, model.ErrUnauthenticated)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	owner := func(r *http.Request) string { u, _ := httpkit.CurrentUser(r.Context()); return u.ID }
	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		out, e := s.Repo.Create(r.Context(), owner(r), model.Schedule{Semester: in.Semester, ScheduleFields: in.ScheduleFields, Source: "USER"})
		if e != nil {
			respondError(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 201, out)
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		var q model.ScheduleQuery
		values := r.URL.Query()
		for key, dest := range map[string]*int{"academic_year": &q.AcademicYear, "term": &q.Term, "teaching_week": &q.TeachingWeek, "limit": &q.Limit, "offset": &q.Offset} {
			if v := values.Get(key); v != "" {
				n, e := strconv.Atoi(v)
				if e != nil {
					respondError(w, r, model.ErrInvalidArgument)
					return
				}
				*dest = n
			}
		}
		for key, dest := range map[string]*time.Time{"from": &q.From, "to": &q.To} {
			if v := values.Get(key); v != "" {
				t, e := time.Parse(time.RFC3339, v)
				if e != nil {
					respondError(w, r, model.ErrInvalidArgument)
					return
				}
				*dest = t
			}
		}
		out, e := s.Repo.List(r.Context(), owner(r), q)
		if e != nil {
			respondError(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": out, "offset": q.Offset, "next_offset": q.Offset + len(out)})
	})
	r.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Repo.Get(r.Context(), owner(r), chi.URLParam(r, "id"))
		if e != nil {
			respondError(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 200, out)
	})
	r.Patch("/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in model.ScheduleUpdate
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		out, e := s.Repo.Update(r.Context(), owner(r), chi.URLParam(r, "id"), in)
		if e != nil {
			respondError(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 200, out)
	})
	r.Delete("/{id}", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Repo.Delete(r.Context(), owner(r), chi.URLParam(r, "id")); e != nil {
			respondError(w, r, e)
			return
		}
		w.WriteHeader(204)
	})
	return r
}

var _ bridge.Port = (*Service)(nil)
