package chaoxing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

const teachingFixture = `<div class="fanyaChapter"><h2>已完成任务点:1/3</h2><div class="chapter_unit"><div class="chapter_item"><div class="catalog_name">第一章<span hidden>PRIVATE_HIDDEN</span></div></div><div class="catalog_level"><ul><li><div class="chapter_item" id="cur11" title="父节点"><div class="catalog_name"><span class="catalog_sbar">1.1</span>父节点</div><div class="catalog_task"><input type="hidden" class="knowledgeJobCount" value="3"><span class="catalog_points_yi">2</span></div></div><div class="catalog_level"><ul><li><div class="chapter_item" id="cur12" title="子节点"><div class="catalog_name">子节点</div><span class="catalog_points_yi">0</span><script>PRIVATE_SCRIPT</script><input value="PRIVATE_TOKEN"></div></li></ul></div></li><li><div class="chapter_item" id="cur13" title="未知进度"><div class="catalog_name">未知进度</div></div></li></ul></div></div></div>`

func TestTeachingTreeNativeIdentityPathsAndEvidence(t *testing.T) {
	rows, err := teachingTree([]byte(teachingFixture), course{ID: "c", Class: "k"})
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if rows[0].Kind != contract.Chapter || rows[1].Kind != contract.Lesson || rows[1].Fields["parent_node_id"] != "11" || rows[0].Fields["task_point_completed"] != 1 || rows[1].Fields["personal_status"] != "completed" || rows[2].Fields["personal_status"] != "unknown" {
		t.Fatal(rows)
	}
	path := rows[1].Fields["path"].([]string)
	if strings.Join(path, "/") != "第一章/父节点/子节点" {
		t.Fatal(path)
	}
	if rows[2].Fields["parent_node_id"] != "" {
		t.Fatal("sibling acquired preceding node as parent", rows[2])
	}
	wire, _ := json.Marshal(rows)
	if strings.Contains(string(wire), "PRIVATE_") {
		t.Fatal(string(wire))
	}
	for _, row := range rows {
		if row.ParentID != `["c","k"]` {
			t.Fatal(row)
		}
	}
}
func TestTeachingBoundedLocalPaginationAndExclusion(t *testing.T) {
	requests := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(1))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, `<input id=enc value=private>`)
		case "/mooc2-ans/mycourse/studentcourse":
			fmt.Fprint(w, `<div class=fanyaChapter>`)
			for i := 0; i < 205; i++ {
				fmt.Fprintf(w, `<div class=chapter_item id=cur%d title=Lesson><div class=catalog_name>Lesson</div></div>`, i+1)
			}
			fmt.Fprint(w, `</div>`)
		default:
			t.Fatal(r.URL.Path)
		}
	})
	p, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Lesson, "")
	if err != nil || len(p.Records) != 100 || !p.HasMore || requests != 3 {
		t.Fatal(len(p.Records), requests, err)
	}
	next, err := a.Sync(context.Background(), testScope, p.Session, contract.Lesson, p.NextCursor)
	if err != nil || len(next.Records) != 100 || !next.HasMore || requests != 3 {
		t.Fatal(len(next.Records), requests, err)
	}
	ctx := contract.WithExcludedCourses(context.Background(), testScope, []string{`["c0","k0"]`})
	final, err := a.Sync(ctx, testScope, next.Session, contract.Lesson, next.NextCursor)
	if err != nil || len(final.Records) != 0 || final.HasMore || requests != 3 {
		t.Fatal(final, requests, err)
	}
}
func TestTeachingDuplicateCountsAndHiddenNodes(t *testing.T) {
	for _, body := range []string{`<div class=chapter_item id=cur1 title=X><span class=catalog_points_yi>bad</span></div>`, `<div class=chapter_item id=cur1 title=X></div><div class=chapter_item id=cur1 title=Y></div>`} {
		if _, err := teachingTree([]byte(body), course{}); err == nil {
			t.Fatal("accepted", body)
		}
	}
	rows, err := teachingTree([]byte(`<div class=fanyaChapter><div hidden><div class=chapter_item id=cur1 title=PRIVATE></div></div></div>`), course{})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
