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
	Label      string         `json:"label"`
	Offering   *int           `json:"offering,omitempty"`
	Item       *int           `json:"item,omitempty"`
	Children   []*Node        `json:"children,omitempty"`
	Count      int            `json:"count"`
	KindCounts map[string]int `json:"kind_counts,omitempty"`
	key        string
	index      map[string]*Node
	position   *float64
	rank       int
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

// directoryMetadata contains only source-provided identities and ordering. A
// display title never replaces an available native ID.
type directoryMetadata struct {
	PathIDs      []string `json:"path_ids"`
	NodeID       string   `json:"nodeId"`
	LegacyNodeID string   `json:"node_id"`
	ParentNodeID string   `json:"parent_node_id"`
	Position     *float64 `json:"position"`
	Order        *float64 `json:"order"`
	TopicID      string   `json:"topic_id"`
	RootID       string   `json:"root_discussion_id"`
	PostID       string   `json:"postId"`
}

func directoryMeta(item model.AcademicItem) directoryMetadata {
	var m directoryMetadata
	_ = json.Unmarshal(item.Metadata, &m)
	if m.NodeID == "" {
		m.NodeID = m.LegacyNodeID
	}
	if m.Position == nil {
		m.Position = m.Order
	}
	return m
}

func catalogItem(item model.AcademicItem) bool {
	switch item.Kind {
	case "chapter", "lesson", "material", "resource", "video", "document", "attachment":
		return true
	case "assignment", "task", "exam", "discussion":
		return item.ChapterID != "" || item.LessonID != "" || len(directoryMeta(item).PathIDs) > 1
	}
	return false
}

func categoryRank(label string) int {
	switch label {
	case "课程目录与资料":
		return 1
	case "作业与任务":
		return 2
	case "考试":
		return 3
	case "通知":
		return 4
	case "讨论":
		return 5
	case "讨论参与与回复记录":
		return 6
	case "学习进度", "课程学习观察":
		return 7
	}
	return 8
}

// naturalLess keeps unnumbered titles readable and orders digit runs numerically
// without parsing opaque IDs or guessing a Chinese chapter number.
func naturalLess(a, b string) bool {
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// Directory preserves every record once. Explicit catalog identities produce
// expandable chapter/lesson records; state and participation stay separate.
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
	// Containers are keyed by offering + source term + native node identity, not
	// by their readable names. Pre-create them so input order cannot duplicate a
	// chapter record as both a folder and a leaf.
	type containerKey struct {
		parent   *Node
		term, id string
	}
	containers := map[containerKey]*Node{}
	parents := make([]*Node, len(tree.Items))
	for i, item := range tree.Items {
		p := offerings[key(item.Source, item.ConnectionID, item.OfferingID)]
		if p == nil {
			p = child(scoped(item.Source, item.ConnectionID), "unassociated", "未关联课程（不推断归属）")
		}
		label := category(item.Kind)
		if catalogItem(item) {
			label = "课程目录与资料"
		}
		p = child(p, "category:"+label, label)
		p.rank = categoryRank(label)
		// A referenced source term is not the owner's enrolled term.
		term := item.SourceTermID
		if term == "" {
			term = item.NativeTermID
		}
		if item.Source == "mooc" && item.SourceTermID != "" && item.SourceTermID != item.NativeTermID {
			p = child(p, "source-term:"+item.SourceTermID, "来源期次 "+line(item.SourceTermID))
		}
		parents[i] = p
		if item.Kind != "chapter" && item.Kind != "lesson" {
			continue
		}
		m := directoryMeta(item)
		id := m.NodeID
		if item.Kind == "chapter" && item.ChapterID != "" {
			id = item.ChapterID
		}
		if item.Kind == "lesson" && item.LessonID != "" {
			id = item.LessonID
		}
		if id == "" && len(m.PathIDs) > 0 {
			id = m.PathIDs[len(m.PathIDs)-1]
		}
		if id == "" {
			continue
		}
		k := containerKey{p, term, id}
		if containers[k] == nil {
			containers[k] = &Node{Label: itemTitle(item), key: "native:" + id}
		}
	}
	topicNames := map[string]string{}
	for _, item := range tree.Items {
		if item.Kind == "discussion" {
			topicNames[key(item.Source, item.ConnectionID, item.NativeID)] = itemTitle(item)
		}
	}
	attached := map[*Node]*Node{}
	attach := func(p, n *Node) bool {
		for ancestor := p; ancestor != nil; ancestor = attached[ancestor] {
			if ancestor == n {
				return false
			}
		}
		if attached[n] == nil {
			if p.index == nil {
				p.index = map[string]*Node{}
			}
			p.index[n.key] = n
			p.Children = append(p.Children, n)
			attached[n] = p
		}
		return true
	}
	for i, item := range tree.Items {
		p, m := parents[i], directoryMeta(item)
		if item.Kind == "discussion_reply_user" {
			topic := m.RootID
			if topic == "" {
				topic = m.TopicID
			}
			if topic == "" {
				topic = m.PostID
			}
			if topic != "" {
				label := topicNames[key(item.Source, item.ConnectionID, topic)]
				p = child(p, "topic:"+topic, named(label, "讨论话题 "+line(topic)))
			}
			idx := i
			p.Children = append(p.Children, &Node{Label: itemTitle(item), Item: &idx, position: m.Position})
			continue
		}
		ancestors := map[*Node]bool{p: true}
		term := item.SourceTermID
		if term == "" {
			term = item.NativeTermID
		}
		var own *Node
		ownID := m.NodeID
		if item.Kind == "chapter" {
			ownID = item.ChapterID
		}
		if item.Kind == "lesson" {
			ownID = item.LessonID
		}
		if ownID == "" {
			ownID = m.NodeID
		}
		if ownID == "" && (item.Kind == "chapter" || item.Kind == "lesson") && len(m.PathIDs) > 0 {
			ownID = m.PathIDs[len(m.PathIDs)-1]
		}
		if item.Kind == "chapter" || item.Kind == "lesson" {
			own = containers[containerKey{parents[i], term, ownID}]
		}
		// Prefer explicit native ancestry, using labels only for a missing node.
		ids := append([]string{}, m.PathIDs...)
		if len(ids) == 0 && catalogItem(item) {
			if item.ChapterID != "" {
				ids = append(ids, item.ChapterID)
			}
			if item.LessonID != "" && item.LessonID != item.ChapterID {
				ids = append(ids, item.LessonID)
			}
		}
		if len(ids) == 0 && m.ParentNodeID != "" && m.ParentNodeID != ownID {
			ids = append(ids, m.ParentNodeID)
		}
		depth := len(item.Path)
		if len(ids) > depth {
			depth = len(ids)
		}
		for j := 0; j < depth; j++ {
			id, title := "", ""
			if j < len(ids) {
				id = ids[j]
			}
			if j < len(item.Path) {
				title = line(item.Path[j])
			}
			// Source paths generally include the current record. Remove only the
			// terminal segment, so repeated ancestor titles remain intact.
			if j == depth-1 && ((ownID != "" && id == ownID) || (id != "" && (id == item.UnitID || id == item.ContentID)) || id == "" && title != "" && title == line(item.Title)) {
				break
			}
			if id == "" && title == "" {
				continue
			}
			k := "path:" + title
			var n *Node
			if id != "" {
				k = "native:" + id
				n = containers[containerKey{parents[i], term, id}]
			}
			if n != nil && (n == own || ancestors[n]) {
				break
			}
			if n == nil {
				n = child(p, k, named(title, "目录节点 "+line(id)))
			} else if !attach(p, n) {
				break
			}
			ancestors[n] = true
			p = n
		}
		idx := i
		if own != nil && own.Item == nil {
			own.Item, own.Label, own.position = &idx, itemTitle(item), m.Position
			if !attach(p, own) {
				// Malformed ancestry must not hide the record or create a cycle.
				own.Item = nil
				p.Children = append(p.Children, &Node{Label: itemTitle(item), Item: &idx, position: m.Position})
			}
		} else {
			p.Children = append(p.Children, &Node{Label: itemTitle(item), Item: &idx, position: m.Position})
		}
	}
	var order func(*Node)
	order = func(n *Node) {
		sort.SliceStable(n.Children, func(i, j int) bool {
			a, b := n.Children[i], n.Children[j]
			if a.rank != b.rank {
				return a.rank < b.rank
			}
			if (a.position != nil) != (b.position != nil) {
				return a.position != nil
			}
			if a.position != nil && b.position != nil && *a.position != *b.position {
				return *a.position < *b.position
			}
			return naturalLess(a.Label, b.Label)
		})
		n.KindCounts = map[string]int{}
		if n.Item != nil {
			n.Count = 1
			n.KindCounts[tree.Items[*n.Item].Kind]++
		}
		for _, c := range n.Children {
			order(c)
			n.Count += c.Count
			for kind, count := range c.KindCounts {
				n.KindCounts[kind] += count
			}
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
