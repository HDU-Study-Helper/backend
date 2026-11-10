// Package academic provides read-only inspection of the current user's academic archive.
package academic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"academic/backend/internal/model"
)

// Node describes a readable directory. Indices refer to the unchanged tree arrays.
// Native identities remain in the records rather than becoming directory names.
type Node struct {
	Label    string  `json:"label"`
	Offering *int    `json:"offering,omitempty"`
	Item     *int    `json:"item,omitempty"`
	Children []*Node `json:"children,omitempty"`
	Count    int     `json:"count"`
	key      string
	index    map[string]*Node
}

func line(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}
func named(s, fallback string) string {
	if s = line(s); s != "" {
		return s
	}
	return fallback
}
func sourceName(s string) string {
	switch s {
	case "chaoxing":
		return "学习通"
	case "mooc":
		return "中国大学 MOOC"
	case "ucampus":
		return "U校园"
	case "jwxt":
		return "杭电教务"
	case "dws":
		return "钉钉"
	}
	return named(s, "未知来源")
}
func kindName(s string) string {
	switch s {
	case "chapter":
		return "章节"
	case "lesson":
		return "课时"
	case "resource", "material", "video", "document", "attachment":
		return "资料"
	case "assignment", "task":
		return "作业与任务"
	case "exam":
		return "考试"
	case "notice", "announcement":
		return "通知"
	case "discussion":
		return "讨论"
	case "progress":
		return "学习进度"
	case "course_observation":
		return "课程学习观察"
	case "discussion_reply_user":
		return "讨论参与与回复记录"
	case "schedule":
		return "课表"
	}
	return named(s, "类型未标明")
}
func itemTitle(item model.AcademicItem) string {
	if line(item.Title) == "" && item.Kind == "course_observation" {
		return "最后访问的单元"
	}
	return named(item.Title, "（标题缺失）")
}
func category(s string) string {
	switch s {
	case "chapter", "lesson", "resource", "material", "video", "document", "attachment":
		return "课程目录与资料"
	}
	return kindName(s)
}
func child(parent *Node, key, label string) *Node {
	if parent.index == nil {
		parent.index = make(map[string]*Node)
	}
	if n := parent.index[key]; n != nil {
		return n
	}
	n := &Node{Label: label, key: key}
	parent.index[key] = n
	parent.Children = append(parent.Children, n)
	return n
}
func terms(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return line(x)
	case []any:
		var parts []string
		for _, entry := range x {
			if m, ok := entry.(map[string]any); ok {
				if name, ok := m["name"].(string); ok && name != "" {
					parts = append(parts, line(name))
					continue
				}
				if id, ok := m["termId"].(string); ok && id != "" {
					parts = append(parts, "期次 "+line(id))
				}
			} else if s, ok := entry.(string); ok && s != "" {
				parts = append(parts, line(s))
			}
		}
		return strings.Join(parts, " · ")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if x[k] != nil && fmt.Sprint(x[k]) != "" {
				parts = append(parts, line(k)+"="+line(fmt.Sprint(x[k])))
			}
		}
		return strings.Join(parts, " · ")
	}
	return ""
}
func courseTypeName(s string) string {
	switch s {
	case "1", "MOOC":
		return "MOOC（公开课）"
	case "2", "SPOC":
		return "SPOC（校内课程）"
	}
	return line(s)
}
func statusName(s string) string {
	switch s {
	case "open":
		return "未完成 / 待提交"
	case "submitted":
		return "已提交"
	case "completed":
		return "已完成"
	case "needs_redo":
		return "需重做"
	case "unknown", "":
		return "未知"
	}
	return line(s)
}
func timeState(s string) string {
	switch s {
	case "known":
		return "已知"
	case "missing":
		return "缺失或未采集"
	case "not_collected":
		return "未采集"
	case "not_applicable":
		return "不适用"
	case "partial":
		return "部分已知"
	case "unknown", "":
		return "未知"
	}
	return line(s)
}
func timePrecision(s string) string {
	switch s {
	case "instant":
		return "精确时刻（带时区）"
	case "local_minute":
		return "本地时间（分钟）"
	case "month_day_minute":
		return "月日与分钟（年份未知）"
	case "day":
		return "日期（未提供时刻）"
	case "unknown", "":
		return "未知"
	}
	return line(s)
}
func offeringName(o model.AcademicOffering) string {
	parts := []string{named(o.Name, "（课程名缺失）")}
	identity := ""
	if o.NativeClassID != "" {
		identity = "教学班 " + line(o.NativeClassID)
	}
	if o.NativeTermID != "" {
		if identity != "" {
			identity += " · "
		}
		identity += "期次 " + line(o.NativeTermID)
	}
	for _, s := range []string{courseTypeName(o.CourseType), identity, o.Teacher, o.School, terms(o.SourceTerms)} {
		if s = line(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// Directory groups every record by source, connection, offering, category and
// source-provided chapter/lesson path. It neither truncates nor infers associations.
func Directory(tree model.AcademicTree) []*Node {
	root := &Node{Children: []*Node{}}
	scoped := func(source, connection string) *Node {
		s := child(root, source, sourceName(source))
		return child(s, connection, "连接 "+named(connection, "（未标明）"))
	}
	offerings := map[string]*Node{}
	key := func(source, connection, id string) string { return source + "\x00" + connection + "\x00" + id }
	for i, o := range tree.Offerings {
		p := scoped(o.Source, o.ConnectionID)
		n := child(p, "offering:"+o.ID, offeringName(o))
		idx := i
		n.Offering = &idx
		offerings[key(o.Source, o.ConnectionID, o.ID)] = n
	}
	for i, item := range tree.Items {
		p := offerings[key(item.Source, item.ConnectionID, item.OfferingID)]
		if p == nil {
			p = child(scoped(item.Source, item.ConnectionID), "unassociated", "未关联课程（不推断归属）")
		}
		p = child(p, "category:"+category(item.Kind), category(item.Kind))
		for _, part := range item.Path {
			if part = line(part); part != "" && part != line(item.Title) {
				p = child(p, "path:"+part, part)
			}
		}
		idx := i
		p.Children = append(p.Children, &Node{Label: itemTitle(item), Item: &idx})
	}
	var order func(*Node)
	order = func(n *Node) {
		sort.SliceStable(n.Children, func(i, j int) bool { return n.Children[i].Label < n.Children[j].Label })
		if n.Item != nil {
			n.Count = 1
		}
		for _, c := range n.Children {
			order(c)
			n.Count += c.Count
		}
	}
	order(root)
	return root.Children
}

var displayZone = time.FixedZone("Asia/Shanghai", 8*60*60)

func timeValue(t model.AcademicTime) string {
	if t.Date != "" && t.Precision == "day" {
		return t.Date
	}
	if t.Value != nil {
		return t.Value.In(displayZone).Format("2006-01-02 15:04:05") + "（上海时间 UTC+08:00）"
	}
	if t.Date != "" {
		return t.Date
	}
	return line(t.Raw)
}
func timeText(t model.AcademicTime) string {
	value := named(timeValue(t), "无值")
	return value + " [状态=" + timeState(t.State) + "；精度=" + timePrecision(t.Precision) + "；字段=" + named(t.SourceField, "未标明") + "]"
}
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "未提供"
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return "格式无效"
	}
	return line(b.String())
}

func coverageText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "覆盖证据未提供；完整性未知"
	}
	words := map[string]string{"requirements": "学习要求", "teachingGoal": "教学目标", "target": "课程目标", "courseLoad": "课程学时 / 学习负担", "courseStyle": "授课方式", "courseName": "课程名称", "description": "课程简介", "courseDescription": "课程介绍", "introduction": "课程介绍", "teachers": "教师团队", "teacher": "教师", "teacherName": "教师姓名", "name": "名称", "school": "学校", "schoolName": "学校名称", "courseTarget": "课程目标", "teachingPlan": "教学计划", "assessment": "考核要求", "credit": "学分", "credits": "学分", "offering": "课程关联", "body": "正文", "title": "标题", "status": "状态", "published": "发布时间", "updated": "更新时间", "start": "开始时间", "end": "结束时间", "deadline": "截止时间", "source_missing_or_not_collected": "缺失或未采集", "source_missing": "源记录未提供", "no_explicit_archived_course_reference": "没有明确的已归档课程引用", "no_interpretable_source_state": "没有可解释的源状态", "material_time_semantics": "资料时间含义", "source_does_not_identify_created_or_updated": "源端未说明是创建时间还是更新时间", "resources": "分类归档", "record_count": "原始证据条数（兼容字段）", "item_count": "逻辑条目数", "evidence_record_count": "原始证据条数", "course_observation": "课程学习观察", "state": "状态", "observed": "已观察到", "not_observed": "尚未观察到", "assignment": "作业", "exam": "考试", "notice": "通知", "discussion": "讨论", "material": "资料", "chapter": "章节", "lesson": "课时", "progress": "进度", "coverage_reason": "覆盖说明", "notice_coverage": "通知覆盖", "assessment_body_coverage": "作业考试正文", "material_body_coverage": "资料正文", "discussion_body_coverage": "讨论正文", "source_term_discovery": "期次发现", "catalog_status": "目录状态", "catalog_chapter_count": "已采集章节数", "scope": "范围", "archived_current_records": "当前已归档记录", "complete_tree": "已返回完整归档目录", "source_completeness": "上游完整性", "not_asserted": "未确认", "limit": "容量上限", "coverage": "覆盖说明", "source_scope": "源端范围", "body_status": "正文采集状态", "unsupported": "未支持", "not_collected": "未采集", "missing": "缺失或未采集", "unknown": "未知", "partial": "部分覆盖", "complete": "已覆盖", "known": "已知", "not_applicable": "不适用"}
	word := func(s string) string {
		if w := words[s]; w != "" {
			return w
		}
		return line(s)
	}
	var render func(any) string
	render = func(value any) string {
		switch x := value.(type) {
		case nil:
			return "未知"
		case string:
			return word(x)
		case bool:
			if x {
				return "是"
			}
			return "否"
		case map[string]any:
			if len(x) == 0 {
				return "覆盖证据未提供；完整性未知"
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, word(k)+"="+render(x[k]))
			}
			return strings.Join(parts, "；")
		case []any:
			if len(x) == 0 {
				return "未提供"
			}
			var parts []string
			for _, entry := range x {
				parts = append(parts, render(entry))
			}
			return strings.Join(parts, "；")
		default:
			return line(fmt.Sprint(x))
		}
	}
	return render(v)
}

// RenderCompactTree exports the entire directory with one line per item. It
// omits empty detail fields while preserving all source/connection/offerings.
func RenderCompactTree(tree model.AcademicTree) string {
	var b strings.Builder
	fmt.Fprintln(&b, "学业归档目录（当前已入库记录；不代表上游全量覆盖）")
	fmt.Fprintf(&b, "课程班级/期次 %d · 条目 %d\n覆盖范围：%s\n", len(tree.Offerings), len(tree.Items), coverageText(tree.Coverage))
	var walk func([]*Node, string)
	walk = func(nodes []*Node, prefix string) {
		for index, n := range nodes {
			branch, next := "├── ", "│   "
			if index == len(nodes)-1 {
				branch, next = "└── ", "    "
			}
			label := line(n.Label)
			if n.Item == nil {
				label += fmt.Sprintf("（%d 条）", n.Count)
			} else {
				x := tree.Items[*n.Item]
				hints := []string{kindName(x.Kind)}
				if x.Status != "" && x.Status != "unknown" {
					hints = append(hints, statusName(x.Status))
				}
				for _, field := range []struct {
					label string
					value model.AcademicTime
				}{{"发布", x.Published}, {"截止", x.Deadline}} {
					t := field.value
					value := timeValue(t)
					if value != "" {
						hints = append(hints, field.label+" "+line(value)+"（"+timePrecision(t.Precision)+"）")
					}
				}
				label += " [" + strings.Join(hints, "；") + "]"
			}
			fmt.Fprintln(&b, prefix+branch+label)
			if n.Offering != nil {
				fmt.Fprintln(&b, prefix+next+"覆盖："+coverageSummary(tree.Offerings[*n.Offering].Coverage))
			}
			walk(n.Children, prefix+next)
		}
	}
	nodes := Directory(tree)
	if len(nodes) == 0 {
		fmt.Fprintln(&b, "尚无已归档课程或条目；完整性未知")
	}
	walk(nodes, "")
	return b.String()
}

func coverageSummary(raw json.RawMessage) string {
	var v struct {
		Resources map[string]struct {
			State string `json:"state"`
			Count int    `json:"record_count"`
			Items *int   `json:"item_count"`
		} `json:"resources"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.Resources) == 0 {
		return "覆盖证据未提供；上游完整性未知"
	}
	keys := make([]string, 0, len(v.Resources))
	for key := range v.Resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		entry := v.Resources[key]
		state := "尚未观察到"
		if entry.State == "observed" {
			state = "已归档"
		}
		if entry.Items != nil {
			parts = append(parts, fmt.Sprintf("%s %d 项（%s）", kindName(key), *entry.Items, state))
		} else {
			parts = append(parts, fmt.Sprintf("%s 原始证据 %d 条（%s）", kindName(key), entry.Count, state))
		}
	}
	return strings.Join(parts, " · ") + "；上游完整性未确认"
}

// RenderTree exports the complete supplied snapshot as a readable plaintext tree.
// It performs no storage access and is reusable by command-line exporters.
func RenderTree(tree model.AcademicTree) string {
	var b strings.Builder
	fmt.Fprintln(&b, "学业归档目录（当前已入库记录；不代表上游全量覆盖）")
	fmt.Fprintf(&b, "课程班级/期次 %d · 条目 %d\n覆盖范围 / 未支持：%s\n", len(tree.Offerings), len(tree.Items), coverageText(tree.Coverage))
	var render func([]*Node, string)
	render = func(nodes []*Node, prefix string) {
		for i, n := range nodes {
			branch, next := "├── ", "│   "
			if i == len(nodes)-1 {
				branch, next = "└── ", "    "
			}
			label := line(n.Label)
			if n.Item == nil {
				label += fmt.Sprintf("（%d 条）", n.Count)
			}
			fmt.Fprintln(&b, prefix+branch+label)
			detail := prefix + next
			if n.Offering != nil {
				o := tree.Offerings[*n.Offering]
				fmt.Fprintln(&b, detail+"课程简介："+named(o.Description, "课程简介缺失或未采集"))
				details := "课程补充信息缺失或未采集"
				if len(o.Details) > 0 && string(o.Details) != "{}" && string(o.Details) != "null" {
					details = coverageText(o.Details)
				}
				fmt.Fprintln(&b, detail+"课程要求与补充信息："+details)
				fmt.Fprintln(&b, detail+"覆盖范围 / 未支持："+coverageText(o.Coverage))
				if len(n.Children) == 0 {
					fmt.Fprintln(&b, detail+"尚无已归档条目；完整性未知")
				}
			}
			if n.Item != nil {
				x := tree.Items[*n.Item]
				fmt.Fprintln(&b, detail+"类型："+kindName(x.Kind)+"；状态："+statusName(x.Status)+"；源状态："+named(x.SourceStatus, "未提供"))
				fmt.Fprintln(&b, detail+"发布："+timeText(x.Published))
				fmt.Fprintln(&b, detail+"更新："+timeText(x.Updated))
				fmt.Fprintln(&b, detail+"开始："+timeText(x.Start))
				fmt.Fprintln(&b, detail+"结束："+timeText(x.End))
				fmt.Fprintln(&b, detail+"截止："+timeText(x.Deadline))
				if x.Text != "" {
					fmt.Fprintln(&b, detail+"正文："+line(x.Text))
				}
				fmt.Fprintln(&b, detail+"个人状态："+rawText(x.PersonalState))
				missing := "没有单列缺失说明；不代表完整采集"
				if len(x.Missing) > 0 && string(x.Missing) != "{}" && string(x.Missing) != "null" {
					missing = coverageText(x.Missing)
				}
				fmt.Fprintln(&b, detail+"缺失说明："+missing)
			}
			render(n.Children, detail)
		}
	}
	nodes := Directory(tree)
	if len(nodes) == 0 {
		fmt.Fprintln(&b, "尚无已归档课程或条目；完整性未知")
	}
	render(nodes, "")
	return b.String()
}
