package chaoxing

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

const assignmentTaskPath = "/mooc-ans/mooc2/work/task"
const assignmentViewPath = "/mooc-ans/mooc2/work/view"
const assignmentHeadersPerCall = 5

type assignmentLanding struct {
	Record contract.Record
	URL    string // private navigation evidence, only in encrypted host cursor
}

// safeAssignmentURL permits exactly the observed read-only task/view routes on
// the list origin. Every identity parameter must occur once and match the row.
func safeAssignmentURL(raw string, origin *url.URL, co course, workID string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || origin == nil {
		return nil, false
	}
	u = origin.ResolveReference(u)
	if !official(u) || u.Host != origin.Host || u.Fragment != "" || u.RawPath != "" || (u.Path != assignmentTaskPath && u.Path != assignmentViewPath) {
		return nil, false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, false
	}
	for k, expected := range map[string]string{"courseId": co.ID, "classId": co.Class, "workId": workID} {
		if len(q[k]) != 1 || q.Get(k) != expected || expected == "" {
			return nil, false
		}
	}
	// No alternate navigation/action selector can turn a GET into a start/write.
	for k, values := range q {
		switch strings.ToLower(k) {
		case "action", "method", "redirect", "redirecturl", "url", "callback":
			return nil, false
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\x00\r\n") {
				return nil, false
			}
		}
	}
	return u, true
}

func assignmentLandings(body []byte, rows []contract.Record, co course) []assignmentLanding {
	origins, _ := url.Parse("https://mooc1.chaoxing.com")
	targets := map[string]string{}
	d, err := document(body)
	if err == nil {
		walk(d, func(n *html.Node) {
			if n.Data != "li" {
				return
			}
			raw := attr(n, "data")
			parsed, e := url.Parse(raw)
			if e != nil {
				return
			}
			id := parsed.Query().Get("workId")
			if u, ok := safeAssignmentURL(raw, origins, co, id); ok && u.Path == assignmentTaskPath {
				targets[id] = u.String()
			}
		})
	}
	out := make([]assignmentLanding, 0, len(rows))
	for _, r := range rows {
		out = append(out, assignmentLanding{Record: r, URL: targets[r.ExternalID]})
	}
	return out
}

func assignmentHeader(ctx context.Context, c *http.Client, item assignmentLanding, co course) (map[string]any, error) {
	origin, _ := url.Parse("https://mooc1.chaoxing.com")
	target, ok := safeAssignmentURL(item.URL, origin, co, item.Record.ExternalID)
	if !ok || target.Path != assignmentTaskPath {
		return nil, contract.ErrInvalid
	}
	client := *c
	blockedRedirect := false
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 2 || r.Method != http.MethodGet {
			blockedRedirect = true
			return contract.ErrInvalid
		}
		u, valid := safeAssignmentURL(r.URL.String(), origin, co, item.Record.ExternalID)
		if !valid || u.Path != assignmentViewPath {
			blockedRedirect = true
			return contract.ErrInvalid
		}
		return nil
	}
	b, err := request(ctx, &client, http.MethodGet, target.String(), nil)
	if blockedRedirect {
		return nil, contract.ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	if err = checked(b); err != nil {
		return nil, err
	}
	return assignmentHeaderMetadata(b)
}

func syncAssignmentHeaders(ctx context.Context, c *http.Client, v *cursor) ([]contract.Record, error) {
	co := v.Courses[v.Index]
	out := []contract.Record{}
	count := len(v.AssignmentLandings)
	if count > assignmentHeadersPerCall {
		count = assignmentHeadersPerCall
	}
	for _, item := range v.AssignmentLandings[:count] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if contract.CourseExcluded(ctx, v.Scope, co.key()) {
			v.advanceCourse()
			return []contract.Record{}, nil
		}
		f := item.Record.Fields
		if item.URL == "" {
			f["header_status"] = "unavailable"
			f["header_reason"] = "safe_task_url_not_provided"
		} else {
			metadata, err := assignmentHeader(ctx, c, item, co)
			if err != nil {
				// An absent/unrecognized header is a limited observation; upstream/auth/
				// cancellation errors still fail the whole page without cursor advancement.
				if err != contract.ErrParse && err != contract.ErrUnsupported {
					return nil, err
				}
				f["header_status"] = "unavailable"
				f["header_reason"] = "recognized_header_not_provided"
				if err == contract.ErrUnsupported {
					f["header_reason"] = "unsafe_redirect_blocked"
				}
			} else {
				for k, value := range metadata {
					f[k] = value
				}
				f["header_status"] = "provided"
				f["coverage"] = "assignment_list_and_header"
			}
		}
		out = append(out, item.Record)
	}
	v.AssignmentLandings = v.AssignmentLandings[count:]
	if len(v.AssignmentLandings) == 0 && v.AssignmentCourseDone {
		v.advanceCourse()
	}
	return out, nil
}
