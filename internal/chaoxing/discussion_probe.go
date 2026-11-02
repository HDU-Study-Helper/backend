package chaoxing

import (
	"context"
	"net/http"
	"strings"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

// DiscussionProbe is a read-only diagnostic result. It contains source text, never session secrets.
type DiscussionProbe struct {
	CourseID     string            `json:"course_id"`
	ClassID      string            `json:"class_id"`
	CourseName   string            `json:"course_name"`
	Status       string            `json:"status"`
	BBSAvailable bool              `json:"bbs_available"`
	PageBytes    int               `json:"page_bytes"`
	PageTitle    string            `json:"page_title"`
	PageText     string            `json:"page_text"`
	Topics       []DiscussionTopic `json:"topics"`
	Scripts      []string          `json:"scripts"`
	Navigation   map[string]bool   `json:"navigation_fields"`
}
type DiscussionTopic struct {
	ID     string         `json:"id"`
	Text   string         `json:"text"`
	Title  string         `json:"title,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// ProbeCourseDiscussion shares the exact navigation and list request with Sync.
func ProbeCourseDiscussion(ctx context.Context, deps contract.Dependencies, scope contract.Scope, session contract.Session, name string) (DiscussionProbe, error) {
	result := DiscussionProbe{Status: "not_found", Topics: []DiscussionTopic{}, Scripts: []string{}, Navigation: map[string]bool{}}
	ad, err := New(deps)
	if err != nil {
		return result, err
	}
	s, c, _, err := ad.(*adapter).restore(scope, session)
	if err != nil {
		return result, err
	}
	if !s.Authorized {
		return result, contract.ErrAuthRequired
	}
	courses, err := fetchCourses(ctx, c)
	if err != nil {
		return result, err
	}
	matches := []course{}
	for _, co := range courses {
		if strings.Contains(strings.ReplaceAll(co.Name, " ", ""), strings.ReplaceAll(name, " ", "")) {
			matches = append(matches, co)
		}
	}
	if len(matches) != 1 {
		if len(matches) > 1 {
			result.Status = "multiple_course_matches"
		}
		return result, nil
	}
	co := matches[0]
	result.CourseID, result.ClassID, result.CourseName = co.ID, co.Class, co.Name
	page, err := openDiscussion(ctx, c, co)
	if err != nil {
		return result, err
	}
	result.BBSAvailable, result.PageBytes = page.BBS != "", len(page.Body)
	d, err := document(page.Body)
	if err != nil {
		return result, err
	}
	result.PageText = text(d)
	if len([]rune(result.PageText)) > 1600 {
		result.PageText = string([]rune(result.PageText)[:1600])
	}
	walk(d, func(n *html.Node) {
		if n.Data == "title" {
			result.PageTitle = text(n)
		}
	})
	result.Status = "page_received"
	var more bool
	result.Topics, _, _, more, err = topicList(ctx, page, co, 1, "", "")
	if err != nil {
		return result, err
	}
	result.Navigation["topic_list_last_page"] = !more
	if len(result.Topics) > 0 {
		result.Status = "topics_found"
	}
	return result, nil
}

type discussionReferer struct {
	base    http.RoundTripper
	referer string
}

func (t discussionReferer) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	if clone.URL.Hostname() == "groupweb.chaoxing.com" {
		clone.Header.Set("Referer", t.referer)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
