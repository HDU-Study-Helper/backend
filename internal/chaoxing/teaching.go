package chaoxing

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

var teachingNativeID = regexp.MustCompile(`^cur([0-9]+)$`)
var teachingCount = regexp.MustCompile(`^[0-9]{1,6}$`)

func teachingVisibleText(n *html.Node) string {
	if n.Type == html.ElementNode && assignmentHeaderHidden(n) {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(teachingVisibleText(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// teachingTree reads the existing studentcourse response, never task cards. A
// title-only unit has no native ID: it is path context, not a fabricated record.
func teachingTree(body []byte, co course) ([]contract.Record, error) {
	d, err := document(body)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []contract.Record{}
	nodes := 0
	var visit func(*html.Node, []string, []string, int) error
	visit = func(n *html.Node, path, ids []string, depth int) error {
		nodes++
		if nodes > 100000 || depth > 128 {
			return contract.ErrParse
		}
		if n.Type == html.ElementNode && assignmentHeaderHidden(n) {
			return nil
		}
		localPath, localIDs := path, ids
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && class(child, "chapter_item") && !assignmentHeaderHidden(child) {
				id := ""
				if m := teachingNativeID.FindStringSubmatch(attr(child, "id")); len(m) == 2 {
					id = m[1]
				}
				title := strings.TrimSpace(attr(child, "title"))
				number := ""
				f := courseFields(co)
				var bad bool
				var scan func(*html.Node)
				scan = func(x *html.Node) {
					if x != child && class(x, "chapter_item") {
						return
					}
					if x.Type == html.ElementNode {
						if class(x, "knowledgeJobCount") && x.Data == "input" {
							raw := attr(x, "value")
							v, e := strconv.Atoi(raw)
							if !teachingCount.MatchString(raw) || e != nil {
								bad = true
							} else {
								f["task_point_total"] = v
								f["task_point_total_source"] = "knowledgeJobCount"
							}
							return
						}
						if assignmentHeaderHidden(x) {
							return
						}
						if class(x, "catalog_name") && title == "" {
							title = teachingVisibleText(x)
						}
						if class(x, "catalog_sbar") {
							number = text(x)
						}
						if class(x, "catalog_points_yi") {
							raw := text(x)
							v, e := strconv.Atoi(raw)
							if !teachingCount.MatchString(raw) || e != nil {
								bad = true
							} else {
								f["task_point_remaining"] = v
								f["task_point_remaining_source"] = "catalog_points_yi"
								if v == 0 {
									f["personal_status"] = "completed"
									f["personal_status_source"] = "catalog_points_yi"
								} else {
									f["personal_status"] = "incomplete"
									f["personal_status_source"] = "catalog_points_yi"
								}
							}
						}
						if class(x, "icon_yiwanc") {
							f["personal_status"] = "completed"
							f["personal_status_source"] = "icon_yiwanc"
						}
					}
					for c := x.FirstChild; c != nil; c = c.NextSibling {
						scan(c)
					}
				}
				scan(child)
				if bad {
					return contract.ErrParse
				}
				if title == "" {
					return contract.ErrParse
				}
				if number != "" {
					title = strings.TrimSpace(strings.TrimPrefix(title, number))
				}
				localPath = append(append([]string{}, path...), title)
				localIDs = append([]string{}, ids...)
				if id != "" {
					if seen[id] || len(out) >= 10000 {
						return contract.ErrParse
					}
					seen[id] = true
					parent := ""
					if len(ids) > 0 {
						parent = ids[len(ids)-1]
					}
					localIDs = append(localIDs, id)
					f["node_id"], f["parent_node_id"], f["path"], f["path_ids"], f["order"], f["coverage"] = id, parent, localPath, localIDs, len(out)+1, "teaching_chapter_tree"
					if number != "" {
						f["number"] = number
					}
					if f["personal_status"] == nil {
						f["personal_status"] = "unknown"
						f["personal_status_reason"] = "not_provided_by_source"
					}
					if total, ok := f["task_point_total"].(int); ok {
						if remaining, ok := f["task_point_remaining"].(int); ok {
							if remaining <= total {
								f["task_point_completed"] = total - remaining
								f["task_point_completed_source"] = "knowledgeJobCount_minus_catalog_points_yi"
							} else {
								f["source_inconsistent"] = true
							}
						}
					}
					// Nested catalog_level belongs to the current item within its li/unit.
					kind := contract.Lesson
					for s := child.NextSibling; s != nil; s = s.NextSibling {
						if class(s, "catalog_level") {
							var native bool
							walk(s, func(c *html.Node) {
								if teachingNativeID.MatchString(attr(c, "id")) {
									native = true
								}
							})
							if native {
								kind = contract.Chapter
							}
							break
						}
					}
					out = append(out, record(kind, id, co.key(), title, "", f))
				}
				if err := visit(child, localPath, localIDs, depth+1); err != nil {
					return err
				}
			} else if err := visit(child, localPath, localIDs, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err = visit(d, nil, nil, 0); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		recognized := progressRatio.MatchString(teachingVisibleText(d))
		walk(d, func(n *html.Node) {
			if class(n, "fanyaChapter") || class(n, "xs_head_name") {
				recognized = true
			}
		})
		if !recognized {
			return nil, contract.ErrParse
		}
	}
	return out, nil
}

func syncTeaching(ctx context.Context, c *http.Client, v *cursor) ([]contract.Record, error) {
	co := v.Courses[v.Index]
	if contract.CourseExcluded(ctx, v.Scope, co.key()) {
		v.advanceCourse()
		return []contract.Record{}, nil
	}
	if len(v.TeachingRecords) == 0 {
		if co.ID == "" || co.Class == "" || co.CPI == "" {
			return nil, contract.ErrParse
		}
		b, err := request(ctx, c, http.MethodGet, middle+"?"+url.Values{"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ismooc2": {"1"}, "v": {"2"}}.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if err = checked(b); err != nil {
			return nil, err
		}
		inputs, err := hidden(b)
		if err != nil || inputs["enc"] == "" {
			return nil, contract.ErrParse
		}
		q := url.Values{"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "enc": {inputs["enc"]}, "openc": {inputs["openc"]}, "fromMiddle": {"1"}, "ut": {"s"}}
		b, err = request(ctx, c, http.MethodGet, "https://mooc2-ans.chaoxing.com/mooc2-ans/mycourse/studentcourse?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		rows, err := teachingTree(b, co)
		if err != nil {
			return nil, err
		}
		if v.Resource == contract.Progress {
			d, err := document(b)
			if err != nil {
				return nil, err
			}
			m := progressRatio.FindStringSubmatch(text(d))
			if len(m) != 3 {
				return nil, contract.ErrParse
			}
			done, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			f := courseFields(co)
			f["completed"], f["total"], f["metric"], f["coverage"] = done, total, "task_points", "course_task_point_aggregate"
			f["node_progress_coverage"] = "native_nodes_on_studentcourse_page"
			if len(rows) == 0 {
				f["node_progress_coverage"] = "not_provided_by_source"
			}
			if done > total {
				f["source_inconsistent"] = true
				f["inconsistency_reason"] = "completed_exceeds_total"
			}
			v.TeachingRecords = append(v.TeachingRecords, record(contract.Progress, "task-points", co.key(), co.Name, "", f))
			for _, row := range rows {
				row.Kind = contract.Progress
				row.ExternalID = "node:" + row.ExternalID
				row.Fields["coverage"] = "personal_node_task_points"
				row.Fields["metric"] = "task_points"
				v.TeachingRecords = append(v.TeachingRecords, row)
			}
		} else {
			for _, row := range rows {
				if row.Kind == v.Resource {
					v.TeachingRecords = append(v.TeachingRecords, row)
				}
			}
		}
	}
	out := v.TeachingRecords
	count := len(out)
	if count > 100 {
		count = 100
	}
	out = out[:count]
	v.TeachingRecords = v.TeachingRecords[count:]
	if len(v.TeachingRecords) == 0 {
		v.advanceCourse()
	}
	return out, nil
}
