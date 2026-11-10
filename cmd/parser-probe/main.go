// parser-probe performs one existing-session read per selected source, without committing records or credentials.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"academic/backend/internal/mooc"
)

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

type trace struct{}

func (trace) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(r)
	out := map[string]any{"host": r.URL.Hostname(), "path": r.URL.Path}
	if err != nil {
		out["transport_error"] = true
		emit(out)
		return res, err
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(b))
	out["status"] = res.StatusCode
	out["bytes"] = len(b)
	out["read_error"] = e != nil
	if r.URL.Hostname() == "www.icourse163.org" {
		var env map[string]any
		if json.Unmarshal(b, &env) == nil {
			out["code"] = env["code"]
			result, _ := env["result"].(map[string]any)
			out["envelope_result_type"] = fmt.Sprintf("%T", env["result"])
			for _, k := range []string{"result", "list", "pagination", "mocTermDto", "chapters"} {
				if v, ok := result[k]; ok {
					out[k+"_type"] = fmt.Sprintf("%T", v)
				}
			}
			rows, _ := result["result"].([]any)
			out["row_count"] = len(rows)
			shapes := []map[string]any{}
			for _, v := range rows {
				m, _ := v.(map[string]any)
				s := map[string]any{}
				for _, k := range []string{"id", "courseId", "name", "termPanel", "schoolPanel", "termId", "schoolSN"} {
					v, ok := m[k]
					s[k+"_present"] = ok
					s[k+"_type"] = fmt.Sprintf("%T", v)
				}
				for _, k := range []string{"termPanel", "schoolPanel"} {
					mm, _ := m[k].(map[string]any)
					for _, f := range []string{"id", "termId", "shortName", "schoolSN"} {
						vv, ok := mm[f]
						s[k+"_"+f+"_present"] = ok
						s[k+"_"+f+"_empty"] = vv == nil || vv == ""
					}
				}
				shapes = append(shapes, s)
			}
			out["rows"] = shapes
			out["pagination"] = result["pagination"]
		}
	}
	if strings.Contains(r.URL.Path, "studentcourse") {
		html := string(b)
		out["task_point_patterns"] = map[string]bool{"old_ratio": regexp.MustCompile(`已完成任务点\s*[:：]\s*([0-9]+)\s*/\s*([0-9]+)`).Match(b), "task_word": strings.Contains(html, "任务点"), "finished_word": strings.Contains(html, "已完成"), "zero_task": strings.Contains(html, "无任务点"), "login_form": strings.Contains(html, "passport2.chaoxing.com/login")}
		for _, p := range []string{`已完成任务点.{0,100}`, `任务点.{0,100}`, `已完成.{0,100}`} {
			matches := regexp.MustCompile(p).FindAllString(html, 4)
			clean := []string{}
			for _, m := range matches {
				m = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(m, " ")
				m = regexp.MustCompile(`[A-Za-z0-9_]{12,}`).ReplaceAllString(m, "[id]")
				clean = append(clean, m)
			}
			out[p] = clean
		}
	}
	emit(out)
	return res, err
}
func main() {
	if err := run(); err != nil {
		emit(map[string]any{"fatal": err.Error()})
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if err != nil {
		return fmt.Errorf("config unavailable")
	}
	defer clear(key)
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer db.Close()
	vault, err := credential.New(key)
	if err != nil {
		return fmt.Errorf("vault unavailable")
	}
	owner := os.Getenv("PROBE_OWNER_ID")
	if owner == "" {
		return fmt.Errorf("PROBE_OWNER_ID required")
	}
	cs, err := db.Platforms().List(ctx, owner)
	if err != nil {
		return fmt.Errorf("connections unavailable")
	}
	deps := contract.Dependencies{HTTPClient: &http.Client{Transport: trace{}}}
	mc, _ := mooc.New(deps)
	cx, _ := chaoxing.New(deps)
	seen := map[string]bool{}
	for _, c := range cs {
		if !c.Enabled || c.State != "active" || seen[c.Source] {
			continue
		}
		source := os.Getenv("PROBE_SOURCE")
		if source != "" && source != c.Source {
			continue
		}
		var adapter contract.Adapter
		resource := contract.Course
		if c.Source == "mooc" {
			adapter = mc
		} else if c.Source == "chaoxing" {
			adapter = cx
			resource = contract.Progress
		} else {
			continue
		}
		seen[c.Source] = true
		scope := model.DWSScope{UserID: owner, ConnectionID: c.ID}
		lease, e := db.Platforms().Acquire(ctx, scope)
		if e != nil {
			return fmt.Errorf("connection busy")
		}
		state, e := db.Platforms().Load(ctx, lease, string(resource))
		_ = db.Platforms().Release(ctx, lease)
		if e != nil {
			return fmt.Errorf("connection state unavailable")
		}
		session, e := vault.Open(owner, c.ID, state.Session)
		if e != nil {
			return fmt.Errorf("session unavailable")
		}
		cursor := ""
		if c.Source == "chaoxing" && len(state.Cursor) > 0 {
			raw, e := vault.Open(owner, c.ID, state.Cursor)
			if e != nil {
				clear(session)
				return fmt.Errorf("checkpoint unavailable")
			}
			cursor = string(raw)
			clear(raw)
		}
		emit(map[string]any{"phase": "start", "source": c.Source, "resource": resource, "has_checkpoint": cursor != ""})
		pages := 1
		if c.Source == "mooc" {
			pages = 20
		}
		for page := 1; page <= pages; page++ {
			p, e := adapter.Sync(ctx, contract.Scope{UserID: owner, ConnectionID: c.ID}, session, resource, cursor)
			result := "ok"
			if e != nil {
				result = e.Error()
			}
			emit(map[string]any{"phase": "result", "source": c.Source, "page": page, "result": result, "records": len(p.Records), "has_more": p.HasMore, "committed": false})
			if e != nil || !p.HasMore {
				clear(p.Session)
				break
			}
			if p.NextCursor == "" || p.NextCursor == cursor {
				clear(p.Session)
				return fmt.Errorf("probe pagination did not advance")
			}
			cursor = p.NextCursor
			if p.Session != nil {
				clear(session)
				session = p.Session
			}
			if page == pages {
				emit(map[string]any{"phase": "budget", "source": c.Source, "incomplete": true})
			} else {
				select {
				case <-ctx.Done():
					clear(session)
					return ctx.Err()
				case <-time.After(1500 * time.Millisecond):
				}
			}
		}
		clear(session)
	}
	return nil
}
