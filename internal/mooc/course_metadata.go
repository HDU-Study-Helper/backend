package mooc

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

// InspectCourseMetadata exposes metadata shape/length only, never page text,
// credentials or links. It uses the same official course owner and private jar.
func InspectCourseMetadata(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, emit func(map[string]any)) error {
	ai, e := New(deps)
	if e != nil {
		return e
	}
	_, h, e := ai.(*adapter).restore(scope, session)
	if e != nil {
		return e
	}
	for typ := 1; typ <= 2; typ++ {
		courses, _, _, e := coursePage(ctx, h, typ, 1)
		if e != nil {
			return e
		}
		if len(courses) == 0 {
			continue
		}
		co := courses[0]
		if typ == 2 {
			for _, candidate := range courses {
				if candidate.ID == "1486679162" {
					co = candidate
				}
			}
		}
		if typ == 2 {
			co.SourceTerm = "1488350464"
			co.Metadata = map[string]any{"sourceCourseId": "1472771170"}
		}
		for _, kind := range []string{"learn", "course", "source_course"} {
			if kind == "source_course" && typ != 2 {
				continue
			}
			u, _ := url.Parse(co.referer())
			path := u.RequestURI()
			if kind == "source_course" {
				path = "/course/" + text(co.Metadata["sourceCourseId"])
			}
			if kind == "course" {
				name := co.ID
				if co.School != "" {
					name = co.School + "-" + co.ID
				}
				path = "/course/" + url.PathEscape(name)
			}
			b, e := request(ctx, h, "GET", path, co.referer(), nil)
			if e != nil {
				emit(map[string]any{"phase": "course_metadata", "type": typ, "page": kind, "error": e.Error()})
				continue
			}
			s := string(b)
			out := map[string]any{"phase": "course_metadata", "type": typ, "page": kind, "bytes": len(b)}
			for _, name := range []string{"courseDto", "termDto", "moocTermDto", "courseCardDto", "courseInfo"} {
				block, e := assignmentObject(s, name)
				if e != nil {
					out[name+"_error"] = e.Error()
					continue
				}
				out[name+"_keys"] = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\s*:`).FindAllString(block, 120)
				out[name+"_bytes"] = len(block)
			}
			doc, e := html.Parse(strings.NewReader(s))
			if e != nil {
				return e
			}
			classes := []map[string]any{}
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if len(classes) > 80 {
					return
				}
				if n.Type == html.ElementNode {
					for _, a := range n.Attr {
						if a.Key == "class" && (strings.Contains(a.Val, "intro") || strings.Contains(a.Val, "teacher") || strings.Contains(a.Val, "description") || strings.Contains(a.Val, "content") || strings.Contains(a.Val, "detail") || strings.Contains(a.Val, "outline") || strings.Contains(a.Val, "rich") || strings.Contains(a.Val, "lector")) {
							body, _ := plainBody(nodeHTMLText(n))
							classes = append(classes, map[string]any{"class": a.Val, "text_length": len(body)})
						}
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			out["metadata_classes"] = classes
			if kind == "learn" {
				f, e := extractLearningTeachers(s)
				if e != nil {
					return e
				}
				out["parsed_teachers"] = f["teacher_coverage"]
				if names, ok := f["teachers"].([]string); ok {
					out["teacher_groups"] = len(names)
				}
			} else {
				expected := co.ID
				if kind == "source_course" {
					expected = text(co.Metadata["sourceCourseId"])
				}
				f, e := extractCourseInfo(s, expected)
				if e != nil {
					return e
				}
				out["description_coverage"] = f["description_coverage"]
				out["description_length"] = len(text(f["course_introduction"]))
			}
			out["quoted_metadata_keys"] = regexp.MustCompile(`(?:description|intro|requirements|teachingGoal|chiefLector|teacher|richContent|courseLoad|courseStyle)["']?\s*:`).FindAllString(s, 40)
			tags := []map[string]any{}
			var tagsWalk func(*html.Node)
			tagsWalk = func(n *html.Node) {
				if n.Type == html.ElementNode && (n.Data == "textarea" || n.Data == "meta") {
					shape := map[string]any{"tag": n.Data, "text_length": len(nodeHTMLText(n))}
					for _, a := range n.Attr {
						if a.Key == "id" || a.Key == "name" {
							shape[a.Key] = a.Val
						}
						if a.Key == "content" {
							shape["content_length"] = len(a.Val)
						}
					}
					tags = append(tags, shape)
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					tagsWalk(c)
				}
			}
			tagsWalk(doc)
			out["metadata_tags"] = tags
			emit(out)
		}
	}
	return nil
}
func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}
func extractLearningTeachers(body string) (map[string]any, error) {
	doc, e := html.Parse(strings.NewReader(body))
	if e != nil {
		return nil, contract.ErrParse
	}
	f := map[string]any{"teacher_coverage": "unsupported: teacher names absent from official learning page"}
	names := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, "learn-teacher-names") {
			raw := strings.Join(strings.Fields(nodeHTMLText(n)), " ")
			if raw != "" && len(raw) < 4096 {
				names = append(names, raw)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(names) > 0 {
		f["teachers"] = names
		f["teacher_coverage"] = "official_learning_page_display_names"
	}
	return f, nil
}
func readCourseInfo(ctx context.Context, h *http.Client, co course) (course, error) {
	if co.Metadata == nil {
		co.Metadata = map[string]any{}
	}
	sourceID := text(co.Metadata["sourceCourseId"])
	if !validID(sourceID) {
		sourceID = co.ID
	}
	name := sourceID
	school := co.SourceSchool
	if sourceID == co.ID {
		school = co.School
	} else if school == "" {
		co.Metadata["description_coverage"] = "unsupported: source course school route not discovered"
		return co, nil
	}
	if school != "" {
		name = school + "-" + sourceID
	}
	b, e := request(ctx, h, "GET", "/course/"+url.PathEscape(name), co.referer(), nil)
	if e != nil {
		return co, e
	}
	fields, e := extractCourseInfo(string(b), sourceID)
	if e != nil {
		return co, e
	}
	if text(fields["course_introduction"]) == "" && text(co.Metadata["course_introduction"]) != "" {
		delete(fields, "description_coverage")
	}
	mergeFields(co.Metadata, fields)
	return co, nil
}
func extractCourseInfo(body, expectedID string) (map[string]any, error) {
	f := map[string]any{"description_coverage": "unsupported: official course introduction not present"}
	block, e := assignmentObject(body, "courseDto")
	if e != nil {
		return nil, e
	}
	ids := pageNumericValues(block, "id")
	if len(ids) != 1 || ids[0] != expectedID {
		return f, nil
	}
	doc, e := html.Parse(strings.NewReader(body))
	if e != nil {
		return nil, contract.ErrParse
	}
	sections := []string{}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode && hasClass(n, "m-infomation_content-section") {
			raw := nodeHTMLText(n)
			s, e := plainBody(raw)
			if e != nil {
				return e
			}
			if s != "" {
				sections = append(sections, s)
			}
			return nil
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if e := walk(c); e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(doc); e != nil {
		return nil, e
	}
	if len(sections) > 20 {
		return nil, contract.ErrParse
	}
	if len(sections) > 0 {
		f["course_introduction"] = strings.Join(sections, "\n\n")
		f["description_coverage"] = "official_course_information_sections"
		f["description_source_course_id"] = expectedID
	}
	return f, nil
}
func nodeHTMLText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
