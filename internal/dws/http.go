package dws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

// Routes returns paths relative to /api/v1/dws. The host must apply session and CSRF middleware.
func (s *Service) Routes() http.Handler {
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
	r.Post("/link", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Body != nil {
			var b [1]byte
			if n, err := r.Body.Read(b[:]); n != 0 || (err != nil && err != io.EOF) {
				respondError(w, r, model.ErrInvalidArgument)
				return
			}
		}
		u, _ := httpkit.CurrentUser(r.Context())
		challenge, err := s.LinkNew(r.Context(), u.ID)
		if err != nil {
			respondError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, http.StatusAccepted, challenge)
	})
	r.Get("/connections", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		items, e := s.repo.ListConnections(r.Context(), u.ID)
		if e != nil {
			respondError(w, r, e)
			return
		}
		if items == nil {
			items = []model.DWSConnection{}
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items})
	})
	r.Post("/connections", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Profile        string        `json:"profile"`
			CorpID         string        `json:"corp_id"`
			UpstreamUserID string        `json:"upstream_user_id"`
			Mode           model.DWSMode `json:"mode"`
		}
		if httpkit.DecodeJSON(w, r, &b) != nil {
			return
		}
		u, _ := httpkit.CurrentUser(r.Context())
		c, e := s.CreateConnection(r.Context(), u.ID, model.DWSConnection{Profile: b.Profile, CorpID: b.CorpID, UpstreamUserID: b.UpstreamUserID, Mode: b.Mode})
		if e != nil {
			respondError(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 201, c)
	})
	r.Route("/connections/{connection}", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			c, e := s.repo.GetConnection(r.Context(), requestScope(r))
			if e != nil {
				respondError(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, c)
		})
		r.Delete("/", func(w http.ResponseWriter, r *http.Request) {
			if e := s.Disconnect(r.Context(), requestScope(r)); e != nil {
				respondError(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 204, nil)
		})
		r.Post("/link", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			challenge, err := s.Link(r.Context(), requestScope(r))
			if err != nil {
				respondError(w, r, err)
				return
			}
			httpkit.WriteJSON(w, http.StatusAccepted, challenge)
		})
		r.Get("/link/{challenge}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			challenge, err := s.LinkStatus(r.Context(), requestScope(r), chi.URLParam(r, "challenge"))
			if err != nil {
				respondError(w, r, err)
				return
			}
			httpkit.WriteJSON(w, http.StatusOK, challenge)
		})
		r.Delete("/link/{challenge}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			challenge, err := s.CancelLink(r.Context(), requestScope(r), chi.URLParam(r, "challenge"))
			if err != nil {
				respondError(w, r, err)
				return
			}
			httpkit.WriteJSON(w, http.StatusOK, challenge)
		})
		r.Get("/groups", func(w http.ResponseWriter, r *http.Request) {
			items, e := s.repo.ListGroups(r.Context(), requestScope(r))
			if e != nil {
				respondError(w, r, e)
				return
			}
			if items == nil {
				items = []model.Group{}
			}
			httpkit.WriteJSON(w, 200, map[string]any{"items": items})
		})
		r.Put("/groups", func(w http.ResponseWriter, r *http.Request) {
			var b struct {
				Groups []model.Group `json:"groups"`
			}
			if httpkit.DecodeJSON(w, r, &b) != nil {
				return
			}
			if len(b.Groups) > 100 {
				respondError(w, r, model.ErrInvalidArgument)
				return
			}
			seen := map[string]bool{}
			for _, g := range b.Groups {
				if g.ID == "" || len(g.ID) > 512 || len(g.Name) > 512 || strings.ContainsAny(g.ID, "\r\n\x00") || seen[g.ID] {
					respondError(w, r, model.ErrInvalidArgument)
					return
				}
				seen[g.ID] = true
			}
			if e := s.repo.SetGroups(r.Context(), requestScope(r), b.Groups); e != nil {
				respondError(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 204, nil)
		})
		r.Post("/session/check", func(w http.ResponseWriter, r *http.Request) {
			status, e := s.CheckSession(r.Context(), requestScope(r))
			if e != nil {
				respondError(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, status)
		})
		r.Post("/sync", func(w http.ResponseWriter, r *http.Request) {
			var b struct {
				GroupID string `json:"group_id"`
			}
			if httpkit.DecodeJSON(w, r, &b) != nil {
				return
			}
			scope := requestScope(r)
			groups, e := s.repo.ListGroups(r.Context(), scope)
			if e != nil {
				respondError(w, r, e)
				return
			}
			allowed := false
			for _, g := range groups {
				if g.ID == b.GroupID {
					allowed = true
				}
			}
			if !allowed {
				respondError(w, r, model.ErrNotFound)
				return
			}
			job, e := s.repo.CreateJob(r.Context(), scope, b.GroupID)
			if e != nil {
				respondError(w, r, e)
				return
			}
			select {
			case s.queue <- task{scope: scope, group: b.GroupID, job: job.ID}:
				httpkit.WriteJSON(w, 202, job)
			default:
				_ = s.repo.FailJob(r.Context(), scope, job.ID, "queue_full")
				httpkit.Error(w, r, 429, "rate_limited", "同步队列已满")
			}
		})
		r.Get("/jobs/{job}", func(w http.ResponseWriter, r *http.Request) {
			job, e := s.repo.GetJob(r.Context(), requestScope(r), chi.URLParam(r, "job"))
			if e != nil {
				respondError(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, job)
		})
		r.Get("/messages", s.messagesHTTP)
		r.Get("/messages/{message}/context", s.contextHTTP)
		r.Post("/messages/{message}/attachments/{attachment}/archive", s.archiveHTTP)
		r.Get("/messages/{message}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
			id, e := strconv.ParseInt(chi.URLParam(r, "message"), 10, 64)
			if e != nil {
				respondError(w, r, model.ErrInvalidArgument)
				return
			}
			a, e := s.repo.GetAttachment(r.Context(), requestScope(r), id, chi.URLParam(r, "attachment"))
			if e != nil {
				respondError(w, r, e)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename=archive.bin")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write(a.Data)
		})
	})
	return r
}
func requestScope(r *http.Request) model.DWSScope {
	u, _ := httpkit.CurrentUser(r.Context())
	return model.DWSScope{UserID: u.ID, ConnectionID: chi.URLParam(r, "connection")}
}
func respondError(w http.ResponseWriter, r *http.Request, e error) {
	code := safeCode(e)
	status := 502
	switch code {
	case "not_found":
		status = 404
	case "invalid_argument":
		status = 400
	case "conflict", "auth_required":
		status = 409
	case "unsupported", "budget_exceeded":
		status = 422
	}
	httpkit.Error(w, r, status, code, "DWS 操作未完成")
}
func (s *Service) messagesHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 20
	if q.Get("limit") != "" {
		v, e := strconv.Atoi(q.Get("limit"))
		if e != nil || v < 1 || v > 100 {
			respondError(w, r, model.ErrInvalidArgument)
			return
		}
		limit = v
	}
	after := int64(0)
	if q.Get("after") != "" {
		v, e := strconv.ParseInt(q.Get("after"), 10, 64)
		if e != nil || v < 0 {
			respondError(w, r, model.ErrInvalidArgument)
			return
		}
		after = v
	}
	if q.Get("group_id") == "" {
		respondError(w, r, model.ErrInvalidArgument)
		return
	}
	items, e := s.repo.ListMessages(r.Context(), requestScope(r), q.Get("group_id"), after, limit)
	if e != nil {
		respondError(w, r, e)
		return
	}
	if items == nil {
		items = []model.Message{}
	}
	dto, e := messageDTOs(items)
	if e != nil {
		respondError(w, r, e)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": dto})
}

func messageDTOs(items []model.Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		if !json.Valid(m.Content) {
			return nil, fmt.Errorf("parse_failed")
		}
		out = append(out, map[string]any{"id": m.ID, "group_id": m.GroupID, "external_id": m.ExternalID, "source_version": m.SourceVersion, "content_hash": m.ContentHash, "content": json.RawMessage(m.Content), "observed_at": m.ObservedAt})
	}
	return out, nil
}

// contextHTTP returns only same-group source observations, preserving immutable IDs and hashes.
func (s *Service) contextHTTP(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(chi.URLParam(r, "message"), 10, 64)
	group := r.URL.Query().Get("group_id")
	if e != nil || id <= 0 || group == "" {
		respondError(w, r, model.ErrInvalidArgument)
		return
	}
	all, e := s.readBounded(r.Context(), requestScope(r), group)
	if e != nil {
		respondError(w, r, e)
		return
	}
	index := -1
	for i, m := range all {
		if m.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		respondError(w, r, model.ErrNotFound)
		return
	}
	start, end := index-3, index+4
	if start < 0 {
		start = 0
	}
	if end > len(all) {
		end = len(all)
	}
	dto, e := messageDTOs(all[start:end])
	if e != nil {
		respondError(w, r, e)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": dto, "relation": "same_group_neighbors", "verification": "unconfirmed"})
}
func (s *Service) readBounded(ctx context.Context, scope model.DWSScope, group string) ([]model.Message, error) {
	out := []model.Message{}
	after := int64(0)
	for page := 0; page < 100; page++ {
		items, e := s.repo.ListMessages(ctx, scope, group, after, 100)
		if e != nil {
			return nil, e
		}
		out = append(out, items...)
		if len(items) < 100 {
			return out, nil
		}
		next := items[len(items)-1].ID
		if next <= after {
			return nil, fmt.Errorf("parse_failed")
		}
		after = next
	}
	return nil, fmt.Errorf("budget_exceeded")
}
func attachmentCategory(a model.Attachment) string {
	if strings.HasPrefix(a.MediaType, "image/") {
		return "image"
	}
	ext := strings.ToLower(filepath.Ext(a.Name))
	switch ext {
	case ".zip", ".rar", ".7z", ".tar", ".gz":
		return "archive"
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".txt", ".md":
		return "document"
	}
	return "other"
}

func resourceKey(ref ResourceRef) string {
	ref.AttachmentID = ""
	b, _ := json.Marshal(ref)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}
func (s *Service) archiveHTTP(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(chi.URLParam(r, "message"), 10, 64)
	if e != nil || id <= 0 {
		respondError(w, r, model.ErrInvalidArgument)
		return
	}
	a, e := s.ArchiveAttachment(r.Context(), requestScope(r), r.URL.Query().Get("group_id"), id, chi.URLParam(r, "attachment"))
	if e != nil {
		respondError(w, r, e)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"attachment": a, "category": attachmentCategory(a), "semantics": "current_observation_not_historical_original"})
}

// ArchiveAttachment archives the first observed current bytes, never a claimed historical original.
func (s *Service) ArchiveAttachment(ctx context.Context, scope model.DWSScope, group string, messageID int64, key string) (model.Attachment, error) {
	var result model.Attachment
	if group == "" {
		return result, model.ErrInvalidArgument
	}
	if a, e := s.repo.GetAttachment(ctx, scope, messageID, key); e == nil {
		return a, nil
	} else if !errors.Is(e, model.ErrNotFound) {
		return result, e
	}
	e := s.withWorkspace(ctx, scope, "", func(ctx context.Context, c model.DWSConnection, l model.Lease, root string) error {
		items, e := s.readBounded(ctx, scope, group)
		if e != nil {
			return e
		}
		var target *model.Message
		for i := range items {
			if items[i].ID == messageID {
				target = &items[i]
				break
			}
		}
		if target == nil {
			return model.ErrNotFound
		}
		var o Observation
		if json.Unmarshal(target.Content, &o) != nil || o.ConversationID != group {
			return fmt.Errorf("parse_failed")
		}
		var ref *ResourceRef
		for i := range o.ResourceRefs {
			if resourceKey(o.ResourceRefs[i]) == key {
				ref = &o.ResourceRefs[i]
				break
			}
		}
		if ref == nil || !ref.Downloadable {
			return model.ErrNotFound
		}
		if ref.MessageID != target.ExternalID || ref.OpenConversationID != group || (ref.Type != "fileId" && ref.Type != "mediaId") {
			return model.ErrInvalidArgument
		}
		// Recheck after acquiring the lease to ensure first-write semantics on retries.
		if a, e := s.repo.GetAttachment(ctx, scope, messageID, key); e == nil {
			result = a
			return nil
		} else if !errors.Is(e, model.ErrNotFound) {
			return e
		}
		output := filepath.Join(root, "downloads", "opaque.bin")
		if e = os.MkdirAll(filepath.Dir(output), 0700); e != nil {
			return e
		}
		defer os.RemoveAll(filepath.Dir(output))
		response, e := s.runner.execute(ctx, root, []string{"chat", "+messages-resource-download", "--type", ref.Type, "--resource-id", ref.ResourceID, "--message-id", ref.MessageID, "--open-conversation-id", group, "--output", "downloads/opaque.bin", "--profile", c.Profile, "-f", "json"}, output)
		if e != nil {
			return e
		}
		var receipt struct {
			MessageID    string `json:"messageId"`
			ResourceID   string `json:"resourceId"`
			ResourceType string `json:"resourceType"`
			LocalPath    string `json:"localPath"`
			SizeBytes    *int64 `json:"sizeBytes"`
		}
		if json.Unmarshal(response, &receipt) != nil || receipt.MessageID != ref.MessageID || receipt.ResourceID != ref.ResourceID || receipt.ResourceType != ref.Type || receipt.LocalPath != "downloads/opaque.bin" || receipt.SizeBytes == nil {
			return fmt.Errorf("parse_failed")
		}
		info, e := os.Lstat(output)
		if e != nil || !info.Mode().IsRegular() || info.Size() > model.MaxAttachmentBytes || info.Size() != *receipt.SizeBytes {
			return fmt.Errorf("attachment_invalid")
		}
		f, e := os.Open(output)
		if e != nil {
			return e
		}
		defer f.Close()
		data, e := io.ReadAll(io.LimitReader(f, model.MaxAttachmentBytes+1))
		if e != nil || len(data) > model.MaxAttachmentBytes {
			return fmt.Errorf("attachment_invalid")
		}
		h := sha256.Sum256(data)
		result = model.Attachment{ID: key, MessageID: messageID, Name: ref.Name, MediaType: http.DetectContentType(data), SHA256: hex.EncodeToString(h[:]), Data: data}
		return s.repo.PutAttachment(ctx, l, result)
	})
	return result, e
}
