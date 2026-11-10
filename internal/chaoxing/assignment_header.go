package chaoxing

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

const assignmentHeaderMaxBody = 4 << 20

var assignmentHeaderLabel = regexp.MustCompile(`(?:作答时间|答题时间|题量|满分)\s*[:：]\s*`)
var assignmentHeaderDate = regexp.MustCompile(`^(?:([0-9]{4})[-/.年])?([0-9]{1,2})[-/.月]([0-9]{1,2})日?(?:[ T]+([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?)?$`)
var assignmentHeaderRange = regexp.MustCompile(`^((?:[0-9]{4}[-/.年])?[0-9]{1,2}[-/.月][0-9]{1,2}日?(?:[ T]+[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?)?)\s*(?:至|~|～)\s*((?:[0-9]{4}[-/.年])?[0-9]{1,2}[-/.月][0-9]{1,2}日?(?:[ T]+[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?)?)$`)
var assignmentHeaderCount = regexp.MustCompile(`^[0-9]{1,5}$`)
var assignmentHeaderScore = regexp.MustCompile(`^[0-9]{1,7}(?:\.[0-9]{1,6})?$`)

// assignmentHeaderMetadata parses an already-read, bounded assignment landing
// page. Only explicit labels in known visible assignment header containers count
// as evidence. It never navigates, reads task content, extracts a title, or infers
// a year from a title or the observation time. Callers merge the returned fields
// into the existing list Record; this helper does not claim body coverage.
// Missing headers, malformed values and conflicting repeated labels fail closed.
func assignmentHeaderMetadata(body []byte) (map[string]any, error) {
	if len(body) == 0 || len(body) > assignmentHeaderMaxBody {
		return nil, contract.ErrParse
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, contract.ErrParse
	}
	fields := map[string]any{}
	var answerRaw, startRaw, endRaw, answerSource string
	var stopped bool
	nodes := 0
	var visit func(*html.Node, bool, int) error
	visit = func(n *html.Node, inHeader bool, depth int) error {
		if stopped {
			return nil
		}
		nodes++
		if nodes > 100000 || depth > 128 {
			return contract.ErrParse
		}
		if n.Type == html.ElementNode {
			if assignmentHeaderHidden(n) {
				return nil
			}
			if inHeader && n.Data == "h2" && class(n, "mark_title") {
				return nil
			}
			if assignmentHeaderContent(n) {
				// A later header-looking node can belong to a question or exam.
				// Never resume scanning beyond the first content boundary.
				stopped = true
				return nil
			}
			inHeader = inHeader || assignmentHeaderContainer(n)
			if inHeader && (n.Data == "p" || n.Data == "div" || n.Data == "span") {
				if line, ok := assignmentHeaderLine(n); ok {
					labels := assignmentHeaderLabel.FindAllStringIndex(line, -1)
					if len(labels) > 0 && labels[0][0] == 0 {
						for i, loc := range labels {
							end := len(line)
							if i+1 < len(labels) {
								end = labels[i+1][0]
							}
							label := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[loc[0]:loc[1]]), ":："))
							value := strings.TrimSpace(line[loc[1]:end])
							switch label {
							case "作答时间", "答题时间":
								m := assignmentHeaderRange.FindStringSubmatch(value)
								if len(m) != 3 || !assignmentHeaderValidDate(m[1]) || !assignmentHeaderValidDate(m[2]) {
									return contract.ErrParse
								}
								if answerRaw != "" && (startRaw != m[1] || endRaw != m[2]) {
									return contract.ErrParse
								}
								if answerRaw == "" {
									answerRaw, startRaw, endRaw, answerSource = value, m[1], m[2], "assignment_header:"+label
								}
							case "题量":
								if !assignmentHeaderCount.MatchString(value) {
									return contract.ErrParse
								}
								v, e := strconv.Atoi(value)
								if e != nil || v > 10000 || !assignmentHeaderSet(fields, "question_count", v, label) {
									return contract.ErrParse
								}
							case "满分":
								if !assignmentHeaderScore.MatchString(value) {
									return contract.ErrParse
								}
								v, e := strconv.ParseFloat(value, 64)
								if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v > 1000000 || !assignmentHeaderSet(fields, "full_score", v, label) {
									return contract.ErrParse
								}
							}
						}
						// Descendants are already represented by this small line.
						return nil
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := visit(c, inHeader, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(doc, false, 0); err != nil || answerRaw == "" {
		return nil, contract.ErrParse
	}
	fields["answer_time_raw"], fields["answer_time_source"] = answerRaw, answerSource
	timeEvidence(fields, "start_at", startRaw, answerSource)
	timeEvidence(fields, "end_at", endRaw, answerSource)
	timeEvidence(fields, "deadline", endRaw, answerSource)
	fields["time_status"] = "provided"
	return fields, nil
}

func assignmentHeaderSet(fields map[string]any, key string, value any, label string) bool {
	if old, exists := fields[key]; exists && old != value {
		return false
	}
	fields[key], fields[key+"_source"] = value, "assignment_header:"+label
	return true
}

func assignmentHeaderContainer(n *html.Node) bool {
	for _, key := range strings.Fields(attr(n, "class") + " " + attr(n, "id")) {
		switch strings.ToLower(key) {
		case "detailshead", "infohead", "ceyan_top", "work-header", "work_header", "workheader", "assignment-header", "assignment_header":
			return true
		}
	}
	return false
}

func assignmentHeaderHidden(n *html.Node) bool {
	switch n.Data {
	case "script", "style", "input", "textarea", "template", "noscript", "svg", "iframe":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "aria-hidden" && strings.EqualFold(strings.TrimSpace(a.Val), "true")) {
			return true
		}
	}
	style := strings.ToLower(strings.Join(strings.Fields(attr(n, "style")), ""))
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
		return true
	}
	for _, key := range strings.Fields(strings.ToLower(attr(n, "class"))) {
		switch key {
		case "hidden", "hide", "dn", "d-none":
			return true
		}
	}
	return false
}

func assignmentHeaderContent(n *html.Node) bool {
	// The observed read-only marking page wraps both header and questions in
	// #fanyaMarking.TiMu. Only this exact outer shell is exempt; descendants
	// still stop traversal at the first question/answer boundary.
	if attr(n, "id") == "fanyaMarking" && class(n, "fanyaMarking") && class(n, "TiMu") {
		return false
	}
	for _, key := range strings.Fields(strings.ToLower(attr(n, "class") + " " + attr(n, "id"))) {
		for _, marker := range []string{"question", "answer", "exam", "work-body", "work_body", "workcontent", "work-content", "ceyan_content", "timu", "ti-mu", "mark_title", "mark_table"} {
			if strings.Contains(key, marker) {
				return true
			}
		}
	}
	return false
}

// A metadata line may contain inline formatting, but never other block lines,
// links, hidden data or task-content containers. Bounds prevent whole-header or
// body text from becoming a candidate just because it begins with a label.
func assignmentHeaderLine(n *html.Node) (string, bool) {
	var b strings.Builder
	var collect func(*html.Node, int) bool
	collect = func(c *html.Node, depth int) bool {
		if depth > 16 || b.Len() > 2048 {
			return false
		}
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
			return b.Len() <= 2048
		}
		if c.Type == html.ElementNode {
			if assignmentHeaderHidden(c) || assignmentHeaderContent(c) {
				return false
			}
			switch c.Data {
			case "span", "b", "strong", "em", "i", "small", "label":
			default:
				return false
			}
		}
		for ch := c.FirstChild; ch != nil; ch = ch.NextSibling {
			if !collect(ch, depth+1) {
				return false
			}
			b.WriteByte(' ')
		}
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !collect(c, 0) {
			return "", false
		}
		b.WriteByte(' ')
	}
	line := strings.Join(strings.Fields(b.String()), " ")
	return line, utf8.RuneCountInString(line) <= 500
}

func assignmentHeaderValidDate(raw string) bool {
	m := assignmentHeaderDate.FindStringSubmatch(raw)
	if len(m) != 7 {
		return false
	}
	values := make([]int, 6)
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			values[i-1], _ = strconv.Atoi(m[i])
		}
	}
	// A leap year validates a yearless month/day without assigning that year to
	// the evidence. Only the untouched source string leaves this helper.
	year := values[0]
	if m[1] == "" {
		year = 2000
	} else if year == 0 {
		return false
	}
	d := time.Date(year, time.Month(values[1]), values[2], values[3], values[4], values[5], 0, time.UTC)
	return d.Year() == year && int(d.Month()) == values[1] && d.Day() == values[2] && d.Hour() == values[3] && d.Minute() == values[4] && d.Second() == values[5]
}
