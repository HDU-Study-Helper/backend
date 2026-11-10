// chaoxing-inspect reads existing authorized list endpoints without committing records or sessions.
// Output is bounded metadata evidence; credentials, navigation URLs and task details are never emitted.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"academic/backend/internal/chaoxing"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"academic/backend/internal/model"
	"golang.org/x/net/html"
)

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

var urlText = regexp.MustCompile(`https?://[^\s<>"']+`)

func safe(s string) string {
	s = urlText.ReplaceAllString(strings.Join(strings.Fields(s), " "), "[url]")
	r := []rune(s)
	if len(r) > 400 {
		s = string(r[:400])
	}
	return s
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Data == "script" || n.Data == "style" {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(text(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

type trace struct {
	samples        int
	headerRead     bool
	assignmentKeys map[string]bool
}

func assignmentKey(u *url.URL) string {
	q := u.Query()
	parts := []string{u.Host, q.Get("courseId"), q.Get("classId"), q.Get("workId")}
	for _, key := range []string{"courseId", "classId", "workId"} {
		if len(q[key]) != 1 || q.Get(key) == "" {
			return ""
		}
	}
	return strings.Join(parts, "\x00")
}

func (t *trace) RoundTrip(r *http.Request) (*http.Response, error) {
	p := r.URL.Path
	allowed := r.Method == http.MethodGet && (p == "/mycourse/backclazzdata" || p == "/visit/stucoursemiddle" || p == "/mooc2-ans/mycourse/stu" || p == "/mooc2-ans/mycourse/studentcourse" || p == "/mooc-ans/mooc2/work/list" || p == "/mooc2-ans/coursedata/stu-datalist" || p == "/course/topic/topicList" || p == "/pc/invitation/getReplyList") || r.Method == http.MethodPost && (strings.HasPrefix(p, "/course/topic/") && strings.HasSuffix(p, "/getTopicList") || p == "/pc/notice/getNoticeList")
	if r.Method == http.MethodGet && (p == "/mooc-ans/mooc2/work/task" || p == "/mooc-ans/mooc2/work/view") {
		allowed = t.assignmentKeys[assignmentKey(r.URL)] && assignmentKey(r.URL) != ""
	}
	if !allowed || r.URL.User != nil || (r.URL.Port() != "" && r.URL.Port() != "443") || r.URL.Scheme != "https" || !strings.HasSuffix(r.URL.Hostname(), ".chaoxing.com") {
		emit(map[string]any{"blocked_host": r.URL.Hostname(), "blocked_path": r.URL.Path})
		return nil, errors.New("endpoint outside read-only allowlist")
	}
	res, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, errors.New("upstream request failed")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	res.Body.Close()
	if e != nil || len(b) > 4<<20 {
		return nil, errors.New("upstream response failed")
	}
	res.Body = io.NopCloser(bytes.NewReader(b))
	if res.StatusCode != 200 {
		emit(map[string]any{"status": res.StatusCode})
		return res, nil
	}
	if p == "/mooc-ans/mooc2/work/view" && os.Getenv("INSPECT_HEADER_STRUCTURE") == "1" {
		d, _ := html.Parse(bytes.NewReader(b))
		headers := []any{}
		prefix := []string{}
		reached := false
		walk(d, func(n *html.Node) {
			if reached {
				return
			}
			if strings.Contains(attr(n, "class"), "infoHead") {
				reached = true
			}
			if n.Type == html.ElementNode && len(prefix) < 80 {
				prefix = append(prefix, n.Data+":"+safe(attr(n, "class"))+":"+safe(attr(n, "id")))
			}
		})
		emit(map[string]any{"kind": "header_prefix_structure", "elements": prefix})
		walk(d, func(n *html.Node) {
			if !strings.Contains(attr(n, "class"), "infoHead") {
				return
			}
			ancestors := []string{}
			for parent := n.Parent; parent != nil; parent = parent.Parent {
				ancestors = append(ancestors, safe(attr(parent, "class"))+":"+safe(attr(parent, "id")))
			}
			lines := []string{}
			walk(n, func(x *html.Node) {
				if x.Data == "span" || x.Data == "p" {
					v := text(x)
					if strings.HasPrefix(v, "题量") || strings.HasPrefix(v, "满分") || strings.HasPrefix(v, "作答时间") {
						lines = append(lines, safe(v))
					}
				}
			})
			headers = append(headers, map[string]any{"ancestor_classes": ancestors, "lines": lines})
		})
		emit(map[string]any{"kind": "known_header_structure", "headers": headers})
	}
	if strings.HasSuffix(p, "/getTopicList") || p == "/pc/notice/getNoticeList" {
		var root map[string]json.RawMessage
		_ = json.Unmarshal(b, &root)
		var rows []map[string]json.RawMessage
		if p == "/pc/notice/getNoticeList" {
			var n struct{ List []map[string]json.RawMessage }
			_ = json.Unmarshal(root["notices"], &n)
			rows = n.List
		} else {
			_ = json.Unmarshal(root["datas"], &rows)
		}
		keys := []string{"createTime", "create_time", "createtime", "gmtCreate", "gmt_create", "updateTime", "update_time", "gmtModified", "lastReplyTime", "last_reply_time", "insertTime", "sendTime", "creatorName", "createrName", "createName", "name", "senderName", "sendName", "courseId", "courseid", "clazzId", "classId", "courseName"}
		counts := map[string]int{}
		examples := map[string]any{}
		for _, row := range rows {
			for _, k := range keys {
				if raw, ok := row[k]; ok {
					counts[k]++
					if strings.Contains(strings.ToLower(k), "time") || strings.HasPrefix(k, "gmt") {
						var v any
						_ = json.Unmarshal(raw, &v)
						switch x := v.(type) {
						case string:
							examples[k] = safe(x)
						case float64:
							examples[k] = x
						}
					}
				}
			}
		}
		emit(map[string]any{"kind": "json_metadata", "notice": p == "/pc/notice/getNoticeList", "rows": len(rows), "field_counts": counts, "date_examples": examples})
	}
	if p == "/mooc2-ans/coursedata/stu-datalist" {
		d, _ := html.Parse(bytes.NewReader(b))
		cells := []any{}
		walk(d, func(n *html.Node) {
			if len(cells) >= 5 || !strings.Contains(attr(n, "class"), "dataBody_time_stu") {
				return
			}
			m := map[string]any{"text": safe(text(n))}
			for _, key := range []string{"title", "data-time", "datetime"} {
				if v := attr(n, key); v != "" {
					m[key] = safe(v)
				}
			}
			cells = append(cells, m)
		})
		emit(map[string]any{"kind": "material_time_cells", "cells": cells})
	}
	if p == "/mooc-ans/mooc2/work/list" {
		if t.assignmentKeys == nil {
			t.assignmentKeys = map[string]bool{}
		}
		d, _ := html.Parse(bytes.NewReader(b))
		walk(d, func(n *html.Node) {
			if n.Data != "li" {
				return
			}
			u, e := url.Parse(attr(n, "data"))
			if e != nil {
				return
			}
			u = r.URL.ResolveReference(u)
			if u.Scheme != "https" || u.Host != r.URL.Host || u.Path != "/mooc-ans/mooc2/work/task" {
				return
			}
			q := u.Query()
			if q.Get("courseId") != r.URL.Query().Get("courseId") || q.Get("classId") != r.URL.Query().Get("classId") {
				return
			}
			if key := assignmentKey(u); key != "" {
				t.assignmentKeys[key] = true
			}
		})
	}
	if p == "/mooc-ans/mooc2/work/list" && t.samples < 5 {
		d, _ := html.Parse(bytes.NewReader(b))
		items := []any{}
		walk(d, func(n *html.Node) {
			if n.Data != "li" || attr(n, "data") == "" || !strings.Contains(attr(n, "data"), "/work/task") {
				return
			}
			if target := os.Getenv("INSPECT_ASSIGNMENT_TITLE"); target != "" && !strings.Contains(text(n), target) {
				return
			}
			if len(items) >= 3 {
				return
			}
			nodes := []any{}
			walk(n, func(c *html.Node) {
				if c.Type != html.ElementNode {
					return
				}
				m := map[string]any{"tag": c.Data}
				names := []string{}
				for _, a := range c.Attr {
					names = append(names, a.Key)
				}
				m["attribute_names"] = names
				for _, k := range []string{"starttime", "endtime", "deadline", "data-endtime", "data-starttime", "createtime", "publishtime", "data-time"} {
					if v := attr(c, k); v != "" {
						m[k] = safe(v)
					}
				}
				if x := attr(c, "class"); x != "" {
					m["class"] = safe(x)
				}
				if x := attr(c, "title"); x != "" {
					m["title"] = safe(x)
				}
				if c.Data != "a" && c.Data != "input" && c.Data != "script" && c.Data != "style" {
					m["text"] = safe(text(c))
				}
				nodes = append(nodes, m)
			})

			items = append(items, nodes)
		})
		if len(items) > 0 {
			t.samples++
			emit(map[string]any{"kind": "assignment_dom", "items": items})
		}
	}
	return res, nil
}
func main() {
	if err := run(); err != nil {
		emit(map[string]any{"fatal": "inspect_failed", "error": err.Error()})
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	key, e := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if e != nil {
		return errors.New("credential config unavailable")
	}
	defer clear(key)
	db, e := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return errors.New("database unavailable")
	}
	defer db.Close()
	vault, e := credential.New(key)
	if e != nil {
		return errors.New("vault unavailable")
	}
	cs, e := db.Platforms().Targets(ctx, "", 100)
	if e != nil {
		return errors.New("connections unavailable")
	}
	var co model.PlatformConnection
	matches := 0
	for _, c := range cs {
		if c.Source != "chaoxing" {
			continue
		}
		if id := os.Getenv("INSPECT_CONNECTION_ID"); id != "" && id != c.ID {
			continue
		}
		co = c
		matches++
	}
	if matches > 1 {
		return errors.New("multiple authorized connections; set INSPECT_CONNECTION_ID")
	}
	if co.ID == "" {
		return errors.New("authorized connection unavailable")
	}
	scope := model.DWSScope{UserID: co.UserID, ConnectionID: co.ID}
	lease, e := db.Platforms().Acquire(ctx, scope)
	if e != nil {
		return errors.New("connection busy")
	}
	state, e := db.Platforms().Load(ctx, lease, "")
	_ = db.Platforms().Release(ctx, lease)
	if e != nil {
		return errors.New("session unavailable")
	}
	session, e := vault.Open(co.UserID, co.ID, state.Session)
	if e != nil {
		return errors.New("session decrypt failed")
	}
	defer clear(session)
	tr := &trace{}
	a, e := chaoxing.New(contract.Dependencies{HTTPClient: &http.Client{Transport: tr}})
	if e != nil {
		return e
	}
	if courseID, classID := os.Getenv("INSPECT_COURSE_ID"), os.Getenv("INSPECT_CLASS_ID"); courseID != "" && classID != "" {
		hostScope := contract.Scope{UserID: co.UserID, ConnectionID: co.ID}
		courses, err := a.(contract.DiscussionPartitioner).DiscussionCourses(ctx, hostScope, session)
		if err != nil {
			return err
		}
		excluded := []string{}
		found := false
		for _, course := range courses {
			if course.CourseID == courseID && course.ClassID == classID {
				found = true
				continue
			}
			ref, _ := json.Marshal([]string{course.CourseID, course.ClassID})
			excluded = append(excluded, string(ref))
		}
		if !found {
			return errors.New("target course unavailable")
		}
		ctx = contract.WithExcludedCourses(ctx, hostScope, excluded)
	}
	if os.Getenv("INSPECT_RESOURCE") == "discussion_reply_user" {
		page, err := a.(contract.ReplyUserReader).DiscussionReplyUsers(ctx, contract.Scope{UserID: co.UserID, ConnectionID: co.ID}, session, os.Getenv("INSPECT_COURSE_ID"), os.Getenv("INSPECT_CLASS_ID"), os.Getenv("INSPECT_TOPIC_ID"))
		if err != nil {
			return err
		}
		defer clear(page.Session)
		counts := map[string]int{}
		self := 0
		for _, row := range page.Records {
			for key := range row.Fields {
				counts[key]++
			}
			if row.Fields["is_current_user"] == true {
				self++
			}
		}
		emit(map[string]any{"kind": "reply_participation_coverage", "rows": len(page.Records), "field_counts": counts, "current_user_observed_replies": self, "coverage": "top_level_only", "absence_semantics": "unknown"})
		return nil
	}
	for _, resource := range []contract.Resource{contract.Assignment, contract.Chapter, contract.Lesson, contract.Progress, contract.Discussion, contract.Notice, contract.Material} {
		if only := os.Getenv("INSPECT_RESOURCE"); only != "" && string(resource) != only {
			continue
		}
		cursor := ""
		for i := 0; i < 30; i++ {
			page, err := a.Sync(ctx, contract.Scope{UserID: co.UserID, ConnectionID: co.ID}, session, resource, cursor)
			if err != nil {
				emit(map[string]any{"resource": resource, "error": err.Error()})
				return errors.New("source inspection stopped")
			}
			emit(map[string]any{"resource": resource, "page": i + 1, "records": len(page.Records), "has_more": page.HasMore})
			if resource == contract.Assignment {
				for _, row := range page.Records {
					if target := os.Getenv("INSPECT_ASSIGNMENT_TITLE"); target != "" && strings.Contains(row.Title, target) {
						emit(map[string]any{"kind": "assignment_header_evidence", "fields": row.Fields})
						tr.headerRead = true
					}
				}
			}
			if resource == contract.Chapter || resource == contract.Lesson || resource == contract.Progress {
				counts := map[string]int{}
				for _, row := range page.Records {
					for k := range row.Fields {
						counts[k]++
					}
				}
				emit(map[string]any{"kind": "teaching_field_coverage", "resource": resource, "rows": len(page.Records), "field_counts": counts})
			}
			clear(page.Session)
			if tr.headerRead {
				break
			}
			if !page.HasMore {
				break
			}
			if os.Getenv("INSPECT_SHORT") == "1" && (resource == contract.Assignment && tr.samples >= 3 || resource == contract.Material && i >= 2) {
				emit(map[string]any{"resource": resource, "incomplete": true, "reason": "sample_budget"})
				break
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				return fmt.Errorf("pagination stopped")
			}
			cursor = page.NextCursor
			if resource == contract.Notice && i >= 2 || i == 29 {
				emit(map[string]any{"resource": resource, "incomplete": true, "reason": "page_budget"})
				break
			}
		}
	}
	return nil
}
