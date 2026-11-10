// refresh-probe batches read-only upstream refresh diagnostics without logging credentials or content.
package main

import (
	"academic/backend/internal/app"
	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"academic/backend/internal/mooc"
	"academic/backend/internal/platform"
	"academic/backend/internal/ucampus"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type trace struct{}

func (trace) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, e := http.DefaultTransport.RoundTrip(r)
	path := r.URL.Path
	if strings.HasPrefix(path, "/course/topic/") && strings.HasSuffix(path, "/getTopicList") {
		path = "/course/topic/{bbsid}/getTopicList"
	}
	event := map[string]any{"host": r.URL.Hostname(), "path": path}
	if e != nil {
		event["transport_error"] = true
		event["deadline"] = errors.Is(e, context.DeadlineExceeded)
	} else {
		event["status"] = resp.StatusCode
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		event["read_error"] = readErr != nil
		event["bytes"] = len(b)

		if r.URL.Hostname() == "notice.chaoxing.com" || strings.HasSuffix(path, "/getTopicList") {
			event["schema_diagnosis"] = diagnoseJSON(b, r)
		}
		if r.URL.Hostname() == "www.icourse163.org" {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				event["code"] = m["code"]
				result, ok := m["result"].(map[string]any)
				event["result_object"] = ok
				dto, wrapped := result["mocTermDto"].(map[string]any)
				if !wrapped {
					dto = result
				}
				event["wrapped_dto"] = wrapped
				chapters, ok := dto["chapters"].([]any)
				event["chapters_array"] = ok
				event["chapters_count"] = len(chapters)
				issues := map[string]int{}
				var scan func(any, string)
				scan = func(v any, collection string) {
					if rows, ok := v.([]any); ok {
						for _, row := range rows {
							scan(row, collection)
						}
						return
					}
					m, ok := v.(map[string]any)
					if !ok {
						issues["non_object_node"]++
						return
					}
					if collection == "units" && m["unitId"] == nil && m["id"] == nil {
						issues["unit_without_id"]++
					}
					for _, key := range []string{"chapters", "lessons", "units", "quizs", "quizzes", "homeworks", "exams"} {
						if child, exists := m[key]; exists {
							if child == nil {
								issues[key+"_null"]++
							} else if _, ok := child.([]any); !ok {
								issues[key+"_non_array"]++
							} else {
								scan(child, key)
							}
						}
					}
				}
				scan(dto, "")
				event["directory_issues"] = issues

				// Only fixed schema field names and types, never response values.
				fields := map[string]string{}
				for _, k := range []string{"mocTermDto", "chapters", "mocTerm", "termDto", "id", "termId", "lessons", "units"} {
					if v, ok := result[k]; ok {
						fields[k] = fmt.Sprintf("%T", v)
					}
				}
				event["schema"] = fields
			}
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(event)
	return resp, e
}
func code(e error) string {
	if errors.Is(e, contract.ErrVerificationRequired) {
		return "verification_required"
	}
	if e == nil {
		return "ok"
	}
	for _, x := range []struct {
		e error
		n string
	}{{contract.ErrParse, "parse_failed"}, {contract.ErrUpstream, "upstream_unavailable"}, {contract.ErrUnsupported, "unsupported"}, {contract.ErrAuthRequired, "auth_required"}, {context.DeadlineExceeded, "timeout"}} {
		if errors.Is(e, x.e) {
			return x.n
		}
	}
	return "other_error"
}
func main() {
	if run() != nil {
		fmt.Println(`{"fatal":"probe_setup_or_connection_failed"}`)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cfg, e := app.LoadConfig()
	if e != nil {
		return e
	}
	db, e := dbbridge.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer db.Close()
	user, e := db.Users().GetByUsername(ctx, cfg.BootstrapUsername)
	if e != nil {
		return e
	}
	vault, e := credential.New(cfg.CredentialKey)
	if e != nil {
		return e
	}
	deps := contract.Dependencies{HTTPClient: &http.Client{Transport: trace{}, Timeout: 40 * time.Second}}
	cx, e := chaoxing.New(deps)
	if e != nil {
		return e
	}
	mc, e := mooc.New(deps)
	if e != nil {
		return e
	}
	uc, e := ucampus.New(deps)
	if e != nil {
		return e
	}
	svc, e := platform.New(db.Platforms(), vault, []contract.Adapter{cx, mc, uc})
	if e != nil {
		return e
	}
	cs, e := db.Platforms().List(ctx, user.ID)
	if e != nil {
		return e
	}
	for _, c := range cs {
		if source := os.Getenv("PROBE_SOURCE"); source != "" && c.Source != source {
			continue
		}
		if !c.Enabled || c.State != "active" {
			continue
		}

		if os.Getenv("PROBE_DISCUSSION_CONTINUATIONS") == "1" && c.Source == "chaoxing" {
			scope := model.DWSScope{UserID: user.ID, ConnectionID: c.ID}
			lease, err := db.Platforms().Acquire(ctx, scope)
			if err != nil {
				return err
			}
			state, err := db.Platforms().Load(ctx, lease, "")
			if err != nil {
				_ = db.Platforms().Release(ctx, lease)
				return err
			}
			raw, err := vault.Open(user.ID, c.ID, state.Session)
			if err != nil {
				_ = db.Platforms().Release(ctx, lease)
				return err
			}
			courses, err := cx.(contract.DiscussionPartitioner).DiscussionCourses(ctx, contract.Scope{UserID: user.ID, ConnectionID: c.ID}, raw)
			clear(raw)
			_ = db.Platforms().Release(ctx, lease)
			if err != nil {
				return err
			}
			for _, course := range courses {
				key, _ := json.Marshal([]string{course.CourseID, course.ClassID})
				kind := "discussion:" + base64.RawURLEncoding.EncodeToString(key)
				lease, err := db.Platforms().Acquire(ctx, scope)
				if err != nil {
					return err
				}
				state, err := db.Platforms().Load(ctx, lease, kind)
				_ = db.Platforms().Release(ctx, lease)
				if err != nil {
					return err
				}
				if len(state.Cursor) == 0 {
					continue
				}
				cursor, err := vault.Open(user.ID, c.ID, state.Cursor)
				if err != nil {
					return err
				}
				hasCursor := len(cursor) > 0
				clear(cursor)
				if !hasCursor {
					continue
				}
				err = svc.SyncDiscussionCourse(ctx, scope, course.CourseID, course.ClassID)
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"resource": "discussion_continuation", "result": code(err)})
				if errors.Is(err, contract.ErrVerificationRequired) || errors.Is(err, contract.ErrRateLimited) {
					break
				}
			}
			continue
		}
		if (os.Getenv("PROBE_NOTICE_FRESH") == "1" || os.Getenv("PROBE_FIRST_DISCUSSION") == "1") && c.Source == "chaoxing" {
			scope := model.DWSScope{UserID: user.ID, ConnectionID: c.ID}
			lease, err := db.Platforms().Acquire(ctx, scope)
			if err != nil {
				return err
			}
			state, err := db.Platforms().Load(ctx, lease, "")
			if err != nil {
				_ = db.Platforms().Release(ctx, lease)
				return err
			}
			raw, err := vault.Open(user.ID, c.ID, state.Session)
			if err != nil {
				_ = db.Platforms().Release(ctx, lease)
				return err
			}
			cursor := ""
			session := contract.Session(raw)
			resource := contract.Notice
			if os.Getenv("PROBE_FIRST_DISCUSSION") == "1" {
				resource = contract.Discussion
			}
			for page := 0; page < 6; page++ {
				result, err := cx.Sync(ctx, contract.Scope{UserID: user.ID, ConnectionID: c.ID}, session, resource, cursor)
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"fresh_page": page + 1, "records": len(result.Records), "has_more": result.HasMore, "result": code(err)})
				if err != nil || !result.HasMore {
					break
				}
				cursor = result.NextCursor
				if result.Session != nil {
					session = result.Session
				}
			}
			clear(raw)
			_ = db.Platforms().Release(ctx, lease)
			continue
		}
		var rs []contract.Resource
		switch c.Source {
		case "chaoxing":
			rs = []contract.Resource{contract.Assignment, contract.Discussion, contract.Progress}
		case "mooc":
			rs = []contract.Resource{contract.Assignment, contract.Exam, contract.Progress}
		case "ucampus":
			rs = []contract.Resource{contract.Assignment, contract.Progress}
		default:
			continue
		}

		if requested := os.Getenv("PROBE_RESOURCES"); requested != "" {
			rs = nil
			for _, name := range strings.Split(requested, ",") {
				resource := contract.Resource(name)
				valid := false
				for _, allowed := range []contract.Resource{contract.Course, contract.Assignment, contract.Exam, contract.Progress, contract.Notice, contract.Discussion, contract.Material} {
					if resource == allowed {
						valid = true
					}
				}
				if !valid {
					return contract.ErrInvalid
				}
				rs = append(rs, resource)
			}
		}
		for _, r := range rs {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"source": c.Source, "resource": r, "phase": "start"})
			e = svc.Sync(ctx, model.DWSScope{UserID: user.ID, ConnectionID: c.ID}, r)
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"source": c.Source, "resource": r, "result": code(e)})
			if errors.Is(e, contract.ErrVerificationRequired) || errors.Is(e, contract.ErrRateLimited) {
				break
			}
		}
	}
	return nil
}

// Fixed schema names, type/count information, and pagination booleans only.
func diagnoseJSON(body []byte, req *http.Request) map[string]any {
	out := map[string]any{}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		out["json_valid"] = false
		return out
	}
	out["json_valid"] = true
	var status bool
	out["status_boolean"] = json.Unmarshal(root["status"], &status) == nil
	out["status_true"] = status
	var rows []map[string]json.RawMessage
	var page map[string]json.RawMessage
	if req.URL.Hostname() == "notice.chaoxing.com" {
		var notices map[string]json.RawMessage
		if json.Unmarshal(root["notices"], &notices) != nil || notices == nil {
			out["notices_object"] = false
			return out
		}
		out["notices_object"] = true
		out["rows_array"] = json.Unmarshal(notices["list"], &rows) == nil && string(notices["list"]) != "null"
		page = notices
		var id string
		out["last_get_id_string"] = json.Unmarshal(notices["lastGetId"], &id) == nil
		out["last_get_id_empty"] = id == ""
	} else {
		out["rows_array"] = json.Unmarshal(root["datas"], &rows) == nil && string(root["datas"]) != "null"
		_, out["datas_present"] = root["datas"]
		out["datas_null"] = string(root["datas"]) == "null"
		out["poff_object"] = json.Unmarshal(root["poff"], &page) == nil && page != nil
	}
	pageTypes := map[string]string{}
	for _, key := range []string{"lastPage", "lastValue", "lastAuxValue", "lastGetId"} {
		if raw, exists := page[key]; exists {
			var value any
			_ = json.Unmarshal(raw, &value)
			pageTypes[key] = fmt.Sprintf("%T", value)
			if key == "lastPage" {
				if value == true || value == false || value == float64(0) || value == float64(1) {
					out["last_page"] = value
				}
			}
		}
	}

	if req.URL.Hostname() == "groupweb.chaoxing.com" {
		out["request_page"] = req.URL.Query().Get("page")
		var last any
		_ = json.Unmarshal(page["lastValue"], &last)
		var lastText string
		switch value := last.(type) {
		case string:
			lastText = value
		case float64:
			lastText = fmt.Sprintf("%.0f", value)
		}
		out["repeated_last_value"] = lastText != "" && lastText == req.URL.Query().Get("last_reply_time")
	}
	out["page_types"] = pageTypes
	out["rows_count"] = len(rows)
	issues := map[string]int{}
	seen := map[string]bool{}
	for _, row := range rows {
		var id string
		for _, key := range []string{"uuid", "idCode", "id"} {
			if raw := row[key]; len(raw) > 0 && string(raw) != "null" {
				id = string(raw)
				break
			}
		}
		if id == "" {
			issues["missing_id"]++
		} else if seen[id] {
			issues["duplicate_id"]++
		}
		seen[id] = true
		for _, key := range []string{"title", "content", "createrName", "creatorName", "createName", "name", "createTime", "updateTime", "lastReplyTime", "reply_count", "replyCount", "praise_count", "praiseCount", "isTeacher", "topicType", "isReply"} {
			if raw, exists := row[key]; exists {
				var value any
				_ = json.Unmarshal(raw, &value)
				switch value.(type) {
				case nil:
					issues[key+"_null"]++
				case []any, map[string]any:
					issues[key+"_non_scalar"]++
				}
			}
		}
	}
	out["row_issues"] = issues
	return out
}
