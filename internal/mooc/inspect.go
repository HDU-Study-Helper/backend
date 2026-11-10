package mooc

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"academic/backend/internal/contract"
)

// InspectCatalog reads bounded catalog metadata with an existing session. It never
// persists credentials, checkpoints or records, and never opens assessment papers.
// Values are limited to catalog metadata; cookies, URLs and arbitrary DTOs stay private.
func InspectCatalog(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, emit func(map[string]any)) error {
	ai, err := New(deps)
	if err != nil {
		return err
	}
	a := ai.(*adapter)
	_, h, err := a.restore(scope, session)
	if err != nil {
		return err
	}
	for typ := 1; typ <= 2; typ++ {
		courses, more, _, err := coursePage(ctx, h, typ, 1)
		if err != nil {
			return err
		}
		emit(map[string]any{"phase": "courses", "courseType": typ, "count": len(courses), "more": more})
		for _, co := range courses {
			dto, err := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {co.Term}})
			if err != nil {
				return err
			}
			emit(map[string]any{"phase": "term", "courseId": co.ID, "termId": co.Term, "courseType": co.Type, "shape": inspectShape(dto)})
			if typ == 2 && co.ID == "1486679162" {
				u, _ := url.Parse(origin)
				csrf := ""
				for _, c := range h.Jar.Cookies(u) {
					if c.Name == "NTESSTUDYSI" {
						csrf = c.Value
					}
				}
				b, e := request(ctx, h, "POST", "/web/j/mocAnnouncementRpcBean.getAnnouncementByTermId.rpc?csrfKey="+url.QueryEscape(csrf), co.referer(), url.Values{"termId": {co.Term}})
				if e != nil {
					return e
				}
				var env map[string]any
				if json.Unmarshal(b, &env) != nil {
					return contract.ErrParse
				}
				shape := map[string]any{"code": env["code"], "result_type": fmt.Sprintf("%T", env["result"])}
				if ar, ok := env["result"].([]any); ok {
					shape["count"] = len(ar)
					if len(ar) > 0 {
						ks := []string{}
						for k := range object(ar[0]) {
							ks = append(ks, k)
						}
						sort.Strings(ks)
						shape["keys"] = ks
					}
				}
				emit(map[string]any{"phase": "notice_probe", "shape": shape})
			}
			if typ == 2 {
				u, _ := url.Parse(co.referer())
				b, e := request(ctx, h, http.MethodGet, u.RequestURI(), origin+"/", nil)
				if e != nil {
					return e
				}
				emit(map[string]any{"phase": "page", "courseId": co.ID, "termId": co.Term, "bytes": len(b), "markers": inspectPage(string(b))})
				discovered, e := discoverPage(string(b), co)
				if e != nil {
					return e
				}
				if discovered.ActiveTerm != "" && discovered.ActiveTerm != co.Term {
					discovered.SourceTerm = discovered.ActiveTerm
					source, e := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {discovered.ActiveTerm}})
					if e != nil {
						return e
					}
					emit(map[string]any{"phase": "active_source", "ownerCourseId": co.ID, "routeTermId": co.Term, "sourceTermId": discovered.ActiveTerm, "shape": inspectShape(source)})
				}
				scripts := []string{}
				tok := html.NewTokenizer(strings.NewReader(string(b)))
				for {
					tt := tok.Next()
					if tt == html.ErrorToken {
						if tok.Err() != io.EOF {
							return contract.ErrParse
						}
						break
					}
					if tt == html.StartTagToken {
						tag, has := tok.TagName()
						if string(tag) != "script" {
							continue
						}
						for has {
							k, v, next := tok.TagAttr()
							has = next
							if string(k) != "src" {
								continue
							}
							u, e := url.Parse(string(v))
							if e == nil && !strings.Contains(u.RawQuery, "=") {
								scripts = append(scripts, u.Host+u.Path)
							}
						}
					}
				}
				for _, v := range regexp.MustCompile(`(?:https?:)?//[A-Za-z0-9./_~-]+\.js(?:[A-Za-z0-9./_~-]*)?`).FindAllString(string(b), 60) {
					u, e := url.Parse(v)
					if e == nil {
						scripts = append(scripts, u.Host+u.Path)
					}
				}
				modulePaths := regexp.MustCompile(`[A-Za-z0-9_./-]+(?:html|js)["']?\s*:\s*["'][a-f0-9]{32}`).FindAllString(string(b), 100)
				emit(map[string]any{"phase": "official_script_paths", "paths": scripts, "module_paths": modulePaths})
			}
		}
	}
	return nil
}
func inspectShape(root map[string]any) map[string]any {
	counts := map[string]int{}
	samples := map[string][]map[string]any{}
	keys := map[string]bool{}
	visited := 0
	var walk func(any, string)
	walk = func(v any, kind string) {
		visited++
		if visited > 10000 {
			return
		}
		if ar, ok := v.([]any); ok {
			counts[kind] += len(ar)
			for _, x := range ar {
				walk(x, kind)
			}
			return
		}
		m := object(v)
		if m == nil {
			return
		}
		for k := range m {
			keys[k] = true
		}
		if kind == "units" || kind == "quizs" || kind == "homeworks" || kind == "exams" {
			f := metadata(m, object(m["test"]))
			for _, k := range []string{"id", "unitId", "contentId", "chapterId", "lessonId"} {
				if scalar(m[k]) {
					f[k] = m[k]
				}
			}
			f["has_test"] = object(m["test"]) != nil
			if test := object(m["test"]); test != nil {
				f["test_type"] = test["type"]
			}
			group := kind + ":" + text(m["contentType"])
			if len(samples[group]) < 2 {
				samples[group] = append(samples[group], f)
			}
		}
		for _, k := range []string{"mocTermDto", "chapters", "lessons", "units", "quizs", "quizzes", "homeworks", "exams"} {
			if x, ok := m[k]; ok {
				walk(x, k)
			}
		}
	}
	dto := object(root["mocTermDto"])
	if dto == nil {
		dto = root
	}
	for _, k := range []string{"announcementDtos", "exams"} {
		v := dto[k]
		counts[k+"_present"] = 0
		if v != nil {
			counts[k+"_present"] = 1
		}
		if ar, ok := v.([]any); ok {
			counts[k] = len(ar)
			if len(ar) > 0 {
				for f := range object(ar[0]) {
					keys[k+"."+f] = true
				}
			}
		}
	}
	walk(root, "")
	names := []string{}
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	return map[string]any{"counts": counts, "samples": samples, "keys": names}
}
func inspectPage(body string) map[string]any {
	out := map[string]any{}
	for _, marker := range []string{"activeTermId", "moocTermDto", "termDto", "courseDto", "announcement", "notice", "getNotice", "getAnnouncement"} {
		out[marker] = strings.Count(body, marker)
	}
	// Exact metadata assignments are parsed by the same bounded discovery parser.
	for _, marker := range []string{"activeTermId", "courseId", "termId"} {
		out[marker+"_values"] = numericPageValues(body, marker)
	}
	for _, field := range []string{"moocTermDto", "termDto", "courseDto"} {
		re := regexp.MustCompile(`(?:window\.)?` + field + `\s*=\s*([^\n]{0,300})`)
		shapes := []string{}
		for _, m := range re.FindAllStringSubmatch(body, 4) {
			s := regexp.MustCompile(`[A-Za-z0-9\p{Han}]+`).ReplaceAllStringFunc(m[1], func(x string) string { return "[" + strconv.Itoa(len(x)) + "]" })
			shapes = append(shapes, s)
		}
		out[field+"_assignment_shape"] = shapes
		block, e := assignmentObject(body, field)
		if e == nil {
			out[field+"_id"] = numericPageValues(block, "id")
			out[field+"_termId"] = numericPageValues(block, "termId")
			out[field+"_activeTermId"] = numericPageValues(block, "activeTermId")
			out[field+"_key_names"] = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*\s*:`).FindAllString(block, 30)
		}
	}
	return out
}
