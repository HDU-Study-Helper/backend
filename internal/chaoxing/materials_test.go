package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func materialPagination(page, total int) string {
	return fmt.Sprintf(`<input id=pages value=%d><input id=currentPageNo value=%d><input id=totalPages value=%d><input id=totalPage value=%d>`, page, page, total, total)
}

func TestMaterialsDirectoryTraversalWithoutDownloads(t *testing.T) {
	var calls []string
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(2))
		case "/mooc2-ans/coursedata/stu-datalist":
			q := r.URL.Query()
			key := q.Get("courseid") + ":" + q.Get("dataId") + ":" + q.Get("pages")
			calls = append(calls, key)
			if q.Get("clazzid") != "k"+strings.TrimPrefix(q.Get("courseid"), "c") || q.Get("type") != "1" {
				t.Error("missing course/class or list parameters")
			}
			switch key {
			case "c0:0:1":
				fmt.Fprint(w, `<div class="dataBody"><ul class="dataBody_td" id="folder" dataName="第一章" type="afolder" enc="PRIVATE_ENC" url="PRIVATE_URL"></ul><ul class="dataBody_td" id="syllabus" dataName="教学大纲.pdf" type="pdf" objectId="PRIVATE_OBJECT" loadurl="PRIVATE_DOWNLOAD"><li class="dataBody_size_stu">20 KB</li><li class="dataBody_time_stu">2026-10-01</li></ul></div>当前页共 2 个`)
			case "c0:0:2":
				fmt.Fprint(w, `<div class=dataBody><ul class=dataBody_td id=notes dataName="课堂笔记.docx" type=docx></ul></div>当前页共 1 个`)
			case "c0:folder:1":
				if q.Get("enc") != "PRIVATE_ENC" || q.Get("dataName") != "第一章" {
					t.Error("folder navigation lost")
				}
				fmt.Fprint(w, `<div class=dataBody><ul class=dataBody_td id=slides dataName="课件 &amp; 讲义.pptx" type=pptx><li class=dataBody_creater_stu>教师</li></ul><ul class=dataBody_td id=nested dataName=补充材料 type=afolder></ul></div>`)
			case "c0:nested:1":
				fmt.Fprint(w, `<div class=dataBody><ul class=dataBody_td id=reference dataName=参考.pdf type=pdf></ul></div>`)
			default:
				fmt.Fprint(w, `<div class=dataBody></div>当前页共 0 个`)
			}
			total := 1
			if q.Get("courseid") == "c0" && q.Get("dataId") == "0" {
				total = 2
			}
			page, _ := strconv.Atoi(q.Get("pages"))
			fmt.Fprint(w, materialPagination(page, total))
		default:
			t.Errorf("non-directory request: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	ctx := context.Background()
	session := authorized(testScope)
	var rows []contract.Record
	cursor := ""
	for i := 0; ; i++ {
		if i >= 15 {
			t.Fatal("traversal did not finish")
		}
		page, err := a.Sync(ctx, testScope, session, contract.Material, cursor)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, page.Records...)
		session = page.Session
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("completed traversal retained cursor")
			}
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("cursor failed to advance")
		}
		cursor = page.NextCursor
	}
	if len(rows) != 6 {
		t.Fatalf("missing file/folder metadata: %d", len(rows))
	}
	wantCalls := []string{"c0:0:1", "c0:0:2", "c0:folder:1", "c0:nested:1", "c1:0:1"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatal("incorrect directory/page traversal", calls)
	}
	for _, row := range rows {
		if row.Kind != contract.Material || row.ParentID != `["c0","k0"]` || row.Content != "" {
			t.Fatalf("invalid material identity/body: %+v", row)
		}
	}
	if !reflect.DeepEqual(rows[5].Fields["path"], []string{"第一章", "补充材料", "参考.pdf"}) || rows[5].Fields["parent_data_id"] != "nested" {
		t.Fatal("nested hierarchy lost", rows[5].Fields)
	}
	if rows[3].Title != "课件 & 讲义.pptx" || rows[3].Fields["creator"] != "教师" || rows[1].Fields["size_raw"] != "20 KB" {
		t.Fatal("metadata was not parsed")
	}
	wire, _ := json.Marshal(rows)
	if strings.Contains(string(wire), "PRIVATE_") || strings.Contains(string(wire), "https://") {
		t.Fatal("private navigation or download metadata escaped")
	}
}

func TestMaterialsRejectUnreliablePages(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":                 "<html>unexpected upstream shape</html>",
		"bad row":                 `<div class=dataBody><ul class=dataBody_td id=bad type=pdf></ul></div>`,
		"count mismatch":          `<div class=dataBody></div>当前页共 2 个`,
		"duplicate":               `<div class=dataBody><ul class=dataBody_td id=a dataName=A type=pdf></ul><ul class=dataBody_td id=a dataName=A type=pdf></ul></div>`,
		"permission page":         "参数传递不正确！",
		"missing pagination":      `<div class=dataBody></div>`,
		"inconsistent pagination": `<div class=dataBody></div><input id=totalPages value=2>`,
	} {
		t.Run(name, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mycourse/backclazzdata" {
					fmt.Fprint(w, coursesFixture(1))
				} else {
					fmt.Fprint(w, body)
					if name != "missing pagination" {
						fmt.Fprint(w, materialPagination(1, 1))
					}
				}
			})
			page, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Material, "")
			if !errors.Is(err, contract.ErrParse) || len(page.Records) != 0 || page.NextCursor != "" {
				t.Fatalf("unreliable page was committable: %+v %v", page, err)
			}
		})
	}
}

func TestMaterialsRejectRepeatedPageAndCrossScopeCursor(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mycourse/backclazzdata" {
			fmt.Fprint(w, coursesFixture(1))
		} else {
			fmt.Fprint(w, `<div class=dataBody><ul class=dataBody_td id=file dataName=讲义.pdf type=pdf></ul></div>`)
			page, _ := strconv.Atoi(r.URL.Query().Get("pages"))
			fmt.Fprint(w, materialPagination(page, 2))
		}
	})
	page, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Material, "")
	if err != nil || !page.HasMore {
		t.Fatal("first page", err)
	}
	failed, err := a.Sync(context.Background(), testScope, page.Session, contract.Material, page.NextCursor)
	if !errors.Is(err, contract.ErrParse) || len(failed.Records) != 0 || failed.NextCursor != "" {
		t.Fatal("ignored page parameter accepted", err)
	}
	other := contract.Scope{UserID: "other", ConnectionID: "other"}
	_, err = a.Sync(context.Background(), other, authorized(other), contract.Material, page.NextCursor)
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("cross-scope cursor accepted", err)
	}
	_, err = a.Sync(context.Background(), testScope, page.Session, contract.Course, page.NextCursor)
	if !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("cross-resource cursor accepted", err)
	}
}
