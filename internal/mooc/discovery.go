package mooc

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"academic/backend/internal/contract"
)

// Read numeric scalar assignments without evaluating scripts or retaining page HTML.
func numericPageValues(body, field string) []string {
	re := regexp.MustCompile(`(?:["']?` + regexp.QuoteMeta(field) + `["']?)\s*[:=]\s*["']?([0-9]{1,20})(?:["'\s,;}])`)
	matches := re.FindAllStringSubmatch(body, 17)
	out := []string{}
	seen := map[string]bool{}
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// assignmentObject isolates an official page's small JS data literal without
// running JavaScript. Brace and string handling prevents matching a later script.
func assignmentObject(body, name string) (string, error) {
	re := regexp.MustCompile(`(?:window\.)?` + regexp.QuoteMeta(name) + `\s*=\s*\{`)
	matches := re.FindAllStringIndex(body, 2)
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) > 1 {
		return "", contract.ErrParse
	}
	start := matches[0][1] - 1
	depth := 0
	quote := byte(0)
	escaped := false
	for i := start; i < len(body) && i-start < 256<<10; i++ {
		c := body[i]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			quote = c
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
			if depth == 0 {
				return body[start : i+1], nil
			}
		}
	}
	return "", contract.ErrParse
}
func discoverCourse(ctx context.Context, h *http.Client, co course) (course, error) {
	u, _ := url.Parse(co.referer())
	b, e := request(ctx, h, http.MethodGet, u.RequestURI(), origin+"/", nil)
	if e != nil {
		return co, e
	}
	fields, e := extractLearningTeachers(string(b))
	if e != nil {
		return co, e
	}
	if co.Metadata == nil {
		co.Metadata = map[string]any{}
	}
	mergeFields(co.Metadata, fields)
	user, e := assignmentObject(string(b), "webUser")
	if e != nil {
		return co, e
	}
	if ids := pageNumericValues(user, "id"); len(ids) == 1 && validID(ids[0]) {
		co.CurrentUser = ids[0]
	}
	if co.Type != 2 {
		co.Discovery = "not_applicable"
		return co, nil
	}
	return discoverPage(string(b), co)
}

// pageNumericValues only accepts scalar properties of the outer data object;
// nested school/member IDs and field-looking text inside descriptions are ignored.
func pageNumericValues(block, field string) []string {
	re := regexp.MustCompile(`^\s*["']?` + regexp.QuoteMeta(field) + `["']?\s*:\s*["']?([0-9]{1,20})["']?\s*$`)
	out := []string{}
	depth := 0
	quote := byte(0)
	escaped := false
	start := 1
	for i := 0; i < len(block); i++ {
		c := block[i]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 1 {
				if m := re.FindStringSubmatch(block[start:i]); m != nil {
					out = append(out, m[1])
				}
				start = i + 1
			}
		}
		if depth == 0 && i >= start {
			if m := re.FindStringSubmatch(block[start:i]); m != nil {
				out = append(out, m[1])
			}
		}
	}
	return out
}
func discoverPage(body string, co course) (course, error) {
	co.Discovery = "unsupported: active source term absent from official page metadata"
	// activeTermId is authoritative when exposed; otherwise official moocTermDto.id
	// identifies the source term and termDto.id remains the route owner.
	found := []string{}
	for _, name := range []string{"courseDto", "moocTermDto", "termDto"} {
		block, e := assignmentObject(body, name)
		if e != nil {
			return co, e
		}
		if block == "" {
			continue
		}
		if name == "termDto" {
			ids := pageNumericValues(block, "id")
			if len(ids) > 0 && (len(ids) != 1 || ids[0] != co.Term) {
				return co, contract.ErrParse
			}
		}
		if name == "courseDto" {
			ids := pageNumericValues(block, "id")
			if len(ids) > 0 && (len(ids) != 1 || ids[0] != co.ID) {
				return co, contract.ErrParse
			}
		}
		ids := pageNumericValues(block, "activeTermId")
		if name == "moocTermDto" && len(ids) == 0 {
			ids = pageNumericValues(block, "id")
		}
		for _, id := range ids {
			if validID(id) {
				found = append(found, id)
			}
		}
	}
	active := ""
	for _, id := range found {
		if active != "" && active != id {
			return co, contract.ErrParse
		}
		active = id
	}
	if active != "" {
		co.ActiveTerm = active
		co.Discovery = "official_learning_page"
		if active == co.Term {
			co.Discovery = "official_learning_page_same_term"
		}
	}
	if strings.Contains(body, "passport/logon") && len(found) == 0 {
		co.Discovery = "unsupported: page did not expose source term"
	}
	return co, nil
}
