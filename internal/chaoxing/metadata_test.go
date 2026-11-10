package chaoxing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAssignmentListEvidenceAndMissingDetails(t *testing.T) {
	co := course{ID: "c", Class: "k", Name: "Course"}
	body := `<div>1/3</div><li data="https://mooc1.chaoxing.com/work/task?courseId=c&amp;classId=k&amp;workId=a&amp;enc=PRIVATE" aria-label="第9章作业20261102;未交"><p class="overHidden2">第9章作业20261102</p><div class="time notOver">剩余6小时59分钟</div><a href="/answers?enc=PRIVATE">作答记录</a></li><li data="/work/task?courseId=c&amp;classId=k&amp;workId=b" aria-label="Report;未交"><p>发布时间：2026-11-02 10:30</p><p>截止时间：2026-11-09 17:00</p><p>作业说明：提交实验报告 <b>PDF</b></p></li><li data="/work/task?courseId=c&amp;classId=k&amp;workId=c" aria-label="Old;已完成"></li>`
	rows, total, err := assignments([]byte(body), co)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatal(total, len(rows), err)
	}
	f := rows[0].Fields
	if rows[0].ParentID != `["c","k"]` || f["course_name"] != "Course" || f["deadline_raw"] != "剩余6小时59分钟" || f["deadline_precision"] != "relative" || f["deadline_reason"] != "relative_countdown_only" || f["body_status"] != "not_collected" || rows[0].Content != "" {
		t.Fatal(rows[0])
	}
	if f["published_at_raw"] != nil || f["published_at_reason"] != "not_provided_by_source" {
		t.Fatal("title date fabricated", f)
	}
	f = rows[1].Fields
	if f["published_at_raw"] != "2026-11-02 10:30" || f["deadline_raw"] != "2026-11-09 17:00" || f["deadline_precision"] != "minute" || rows[1].Content != "提交实验报告 PDF" || f["body_status"] != "list_description" {
		t.Fatal(rows[1])
	}
	if rows[2].Fields["deadline_reason"] != "not_provided_by_source" {
		t.Fatal(rows[2])
	}
	wire, _ := json.Marshal(rows)
	if strings.Contains(string(wire), "PRIVATE") || strings.Contains(string(wire), "作答记录") {
		t.Fatal("unsafe task navigation emitted")
	}
}

func TestDiscussionRealSnakeCaseTimeAndAuthorEvidence(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(1))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, discussionMiddleFixture)
		case "/course/topic/topicList":
			fmt.Fprint(w, `<input id=bbsid value=fixture-bbs>`)
		case "/course/topic/fixture-bbs/getTopicList":
			fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"topic","content":"<p>Discuss the chapter</p>","create_time":1789979041576,"update_time":1793892612333,"createrName":"Teacher","last_reply_time":1793892612000,"enc":"PRIVATE","files":[{"url":"PRIVATE"}]}],"poff":{"lastPage":1}}`)
		default:
			t.Fatal("unexpected request")
		}
	})
	p, err := a.Sync(context.Background(), testScope, authorized(testScope), discussionResource, "")
	if err != nil || len(p.Records) != 1 {
		t.Fatal(err)
	}
	f := p.Records[0].Fields
	if f["create_time"] != float64(1789979041576) || f["published_at_raw"] != "1789979041576" || f["published_at_source"] != "create_time" || f["published_at_precision"] != "instant" || f["updated_at_raw"] != "1793892612333" || f["updated_at_source"] != "update_time" || f["last_reply_at_raw"] != "1793892612000" || f["author"] != "Teacher" || f["course_name"] != "Course 0" {
		t.Fatal(f)
	}
	wire, _ := json.Marshal(p.Records)
	if strings.Contains(string(wire), "PRIVATE") {
		t.Fatal("unsafe metadata escaped")
	}
}

func TestNoticeSendTimeHasExplicitMeaningAndNoInventedCourse(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pc/notice/getNoticeList" {
			t.Fatal("unexpected request")
		}
		fmt.Fprint(w, `{"status":true,"notices":{"lastPage":1,"list":[{"uuid":"notice","content":"<p>Announcement</p>","sendTime":"2026-10-26 20:01:07","insertTime":1793016067000,"updateTime":1793791198000,"createrName":"Teacher","courseName":"unverified","url":"PRIVATE"}]}}`)
	})
	rows, _, _, err := notices(context.Background(), &a.client, "")
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	f := rows[0].Fields
	if rows[0].ParentID != "" || f["course_name"] != nil || f["published_at_source"] != "sendTime" || f["published_at_semantics"] != "notice_send_time" || f["published_at_precision"] != "second" || f["inserted_at_raw"] != "1793016067000" || f["updated_at_raw"] != "1793791198000" || f["author"] != "Teacher" {
		t.Fatal(rows[0])
	}
}

func TestMaterialYearlessAndAbsentTimesRemainUnknown(t *testing.T) {
	body := `<div class=dataBody><ul class=dataBody_td id=a dataName=notes.pdf type=pdf><li class=dataBody_time_stu>11-03 12:30</li><li class=dataBody_creater_stu>Teacher</li></ul><ul class=dataBody_td id=b dataName=folder type=afolder></ul></div>` + materialPagination(1, 1)
	rows, _, _, err := parseMaterials([]byte(body), course{ID: "c", Class: "k", Name: "Course"}, materialFolder{ID: "0"}, 1)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	f := rows[0].Fields
	if f["time_raw"] != "11-03 12:30" || f["time_precision"] != "unknown" || f["time_reason"] != "missing_year" || f["time_source"] != "dataBody_time_stu" || f["author"] != "Teacher" || f["published_at_raw"] != nil || f["body_status"] != "not_collected" {
		t.Fatal(f)
	}
	if rows[1].Fields["time_reason"] != "not_provided_by_source" {
		t.Fatal(rows[1])
	}
}
