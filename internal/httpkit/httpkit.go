// Package httpkit provides shared HTTP responses and authenticated request context.
package httpkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5/middleware"
)

type userKey struct{}

func WithUser(ctx context.Context, user model.User) context.Context {
	user.PasswordHash = ""
	return context.WithValue(ctx, userKey{}, user)
}
func CurrentUser(ctx context.Context) (model.User, bool) {
	u, ok := ctx.Value(userKey{}).(model.User)
	return u, ok && u.ID != "" && !u.Disabled
}
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, status, struct {
		Error     any    `json:"error"`
		RequestID string `json:"request_id"`
	}{struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{code, message, status >= 500}, middleware.GetReqID(r.Context())})
}

// DecodeJSON accepts exactly one strict JSON object, capped at 16 KiB.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONLimit(w, r, dst, 16<<10)
}

// DecodeJSONLimit keeps the same strict object semantics for endpoints with a
// reviewed larger payload, such as an Agent definition with a 64 KiB prompt.
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		Error(w, r, 415, "unsupported_media_type", "请使用 application/json")
		return errors.New("unsupported media type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// Reject null, arrays, scalars and trailing JSON while preserving strict field checks.
	var raw json.RawMessage
	if err = dec.Decode(&raw); err != nil {
		Error(w, r, 400, "invalid_argument", "JSON 请求无效或过大")
		return err
	}
	if len(raw) == 0 || raw[0] != '{' {
		Error(w, r, 400, "invalid_argument", "请求必须为 JSON 对象")
		return errors.New("expected object")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err = strict.Decode(dst); err != nil {
		Error(w, r, 400, "invalid_argument", "JSON 字段无效")
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		Error(w, r, 400, "invalid_argument", "请求仅允许一个 JSON 对象")
		return errors.New("trailing JSON")
	}
	return nil
}
