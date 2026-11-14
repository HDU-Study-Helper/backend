package academic

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"academic/backend/internal/model"
)

func directoryRecords(ns []*Node) map[int]int {
	seen := map[int]int{}
	var walk func([]*Node)
	walk = func(nodes []*Node) {
		for _, n := range nodes {
			if n.Item != nil {
				seen[*n.Item]++
			}
			walk(n.Children)
		}
	}
	walk(ns)
	return seen
}

// Opt-in local check of an authenticated read-only API snapshot. Default tests
// remain offline and use the sanitized fixtures below.
func TestDirectoryAPISnapshot(t *testing.T) {
	path := os.Getenv("ACADEMIC_TREE_SNAPSHOT")
	if path == "" {
		t.Skip("no read-only API snapshot supplied")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tree model.AcademicTree
	if err = json.Unmarshal(b, &tree); err != nil {
		t.Fatal(err)
	}
	nodes := Directory(tree)
	seen := directoryRecords(nodes)
	if len(seen) != len(tree.Items) {
		t.Fatalf("found %d/%d records", len(seen), len(tree.Items))
	}
	count := 0
	for _, n := range seen {
		if n != 1 {
			t.Fatalf("record repeated %d times", n)
		}
		count += n
	}
	for _, n := range nodes {
		count -= n.Count
	}
	if count != 0 {
		t.Fatal("root count differs from records")
	}
	t.Logf("API snapshot: %d offerings, %d records; each appears once", len(tree.Offerings), len(tree.Items))
}

func TestDirectoryNativeCatalogAndCounts(t *testing.T) {
	tree := model.AcademicTree{Offerings: []model.AcademicOffering{{ID: "off", Source: "mooc", ConnectionID: "conn", Name: "课程", NativeTermID: "owner"}}}
	item := func(kind, title, chapter, lesson, node string, path, ids []string, position int) model.AcademicItem {
		meta, _ := json.Marshal(map[string]any{"path_ids": ids, "nodeId": node, "position": position})
		return model.AcademicItem{Source: "mooc", ConnectionID: "conn", OfferingID: "off", NativeTermID: "owner", SourceTermID: "owner", Kind: kind, Title: title, ChapterID: chapter, LessonID: lesson, Path: path, Metadata: meta}
	}
	// Units intentionally precede their chapter/lesson records.
	tree.Items = []model.AcademicItem{
		item("material", "资料10", "c", "l", "u10", []string{"同名", "同名", "资料10"}, []string{"c", "l", "u10"}, 10),
		item("assignment", "测验2", "c", "l", "u2", []string{"同名", "同名", "测验2"}, []string{"c", "l", "u2"}, 2),
		item("chapter", "同名", "c", "", "c", []string{"同名"}, []string{"c"}, 1),
		item("lesson", "同名", "c", "l", "l", []string{"同名", "同名"}, []string{"c", "l"}, 1),
		item("exam", "考试", "c", "", "e", []string{"同名", "考试"}, []string{"c", "e"}, 3),
		item("discussion_reply_user", "参与人", "c", "l", "user", nil, nil, 1),
		item("progress", "状态", "c", "l", "progress", nil, nil, 1),
	}
	nodes := Directory(tree)
	seen := directoryRecords(nodes)
	if len(seen) != len(tree.Items) {
		t.Fatalf("lost records: %v", seen)
	}
	for i, n := range seen {
		if n != 1 {
			t.Fatalf("record %d duplicated %d times", i, n)
		}
	}
	offering := nodes[0].Children[0].Children[0]
	if offering.Count != 7 || offering.KindCounts["assignment"] != 1 || offering.KindCounts["exam"] != 1 || offering.KindCounts["material"] != 1 {
		t.Fatalf("bad counts: %+v", offering)
	}
	catalog := offering.Children[0]
	if catalog.Label != "课程目录与资料" || len(catalog.Children) != 1 {
		t.Fatalf("catalog roots %+v", catalog.Children)
	}
	chapter := catalog.Children[0]
	if chapter.Item == nil || *chapter.Item != 2 || len(chapter.Children) != 2 {
		t.Fatalf("chapter duplicated or missing: %+v", chapter)
	}
	lesson := chapter.Children[0]
	if lesson.Item == nil || *lesson.Item != 3 || len(lesson.Children) != 2 {
		t.Fatalf("lesson missing: %+v", lesson)
	}
	if *lesson.Children[0].Item != 1 || *lesson.Children[1].Item != 0 {
		t.Fatal("source position ignored")
	}
	for _, n := range offering.Children[1:] {
		if n.Label == "讨论" || n.Label == "作业与任务" {
			t.Fatalf("state or roster became a topic/task: %s", n.Label)
		}
	}
}
func TestDirectoryRosterGroupedByExplicitTopic(t *testing.T) {
	tree := model.AcademicTree{Items: []model.AcademicItem{
		{Source: "mooc", ConnectionID: "c", Kind: "discussion", NativeID: "opaque-topic", Title: "话题标题"},
		{Source: "mooc", ConnectionID: "c", Kind: "discussion_reply_user", Title: "参与人", Metadata: json.RawMessage(`{"topic_id":"opaque-topic"}`)},
		{Source: "mooc", ConnectionID: "c", Kind: "discussion_reply_user", Title: "其他参与人", Metadata: json.RawMessage(`{"topic_id":"another-topic"}`)},
	}}
	nodes := Directory(tree)
	group := nodes[0].Children[0].Children[0].Children[1]
	if group.Label != "讨论参与与回复记录" || len(group.Children) != 2 {
		t.Fatalf("bad roster: %+v", group)
	}
	labels := map[string]bool{}
	for _, n := range group.Children {
		labels[n.Label] = true
		if n.Count != 1 {
			t.Fatal("topic roster count")
		}
	}
	if !labels["话题标题"] || !labels["讨论话题 another-topic"] {
		t.Fatal(labels)
	}
	if len(directoryRecords(nodes)) != 3 {
		t.Fatal("topic grouping lost or duplicated records")
	}
}

func TestDirectorySameTitlesNativeIDsAndSourceTerms(t *testing.T) {
	tree := model.AcademicTree{Offerings: []model.AcademicOffering{{ID: "o", Source: "mooc", ConnectionID: "c", NativeTermID: "owner"}}}
	for _, v := range []struct{ term, id string }{{"owner", "one"}, {"owner", "two"}, {"reference", "one"}} {
		meta, _ := json.Marshal(map[string]any{"path_ids": []string{v.id}, "nodeId": v.id})
		tree.Items = append(tree.Items, model.AcademicItem{Source: "mooc", ConnectionID: "c", OfferingID: "o", NativeTermID: "owner", SourceTermID: v.term, Kind: "chapter", Title: "相同标题", ChapterID: v.id, Path: []string{"相同标题"}, Metadata: meta})
	}
	nodes := Directory(tree)
	if got := directoryRecords(nodes); !reflect.DeepEqual(got, map[int]int{0: 1, 1: 1, 2: 1}) {
		t.Fatal(got)
	}
	catalog := nodes[0].Children[0].Children[0].Children[0]
	if len(catalog.Children) != 3 {
		t.Fatalf("native IDs or source terms merged: %+v", catalog.Children)
	}
	if !strings.Contains(RenderCompactTree(tree), "来源期次 reference") {
		t.Fatal("reference term hidden")
	}
}
func TestDirectoryNaturalOrderingAndRepeatedPathLabels(t *testing.T) {
	tree := model.AcademicTree{Items: []model.AcademicItem{{Source: "chaoxing", ConnectionID: "c", Kind: "material", Title: "第10节", Path: []string{"同名", "同名", "第10节"}}, {Source: "chaoxing", ConnectionID: "c", Kind: "material", Title: "第2节", Path: []string{"同名", "同名", "第2节"}}}}
	n := Directory(tree)[0].Children[0].Children[0].Children[0].Children[0]
	if n.Label != "同名" || len(n.Children) != 1 || n.Children[0].Label != "同名" {
		t.Fatalf("path lost repeated segment: %+v", n)
	}
	leaves := n.Children[0].Children
	if leaves[0].Label != "第2节" || leaves[1].Label != "第10节" {
		t.Fatal("numeric titles sorted lexically")
	}
}
