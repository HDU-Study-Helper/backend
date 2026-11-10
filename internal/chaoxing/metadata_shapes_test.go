package chaoxing

import (
	"errors"
	"testing"

	"academic/backend/internal/contract"
)

func TestTopicTimeAliasesRemainScalarWhitelist(t *testing.T) {
	for _, fields := range []string{`"gmtCreate":"2026-10-01","gmtModified":"2026-10-02"`, `"createTime":"2026-10-01","updateTime":"2026-10-02"`, `"create_time":1789979041576,"update_time":1793892612333`} {
		topics, _, _, _, err := parseTopics([]byte(`{"status":true,"datas":[{"uuid":"t","content":"Body",` + fields + `}],"poff":{"lastPage":1}}`))
		if err != nil || len(topics) != 1 {
			t.Fatal(err)
		}
		f := topics[0].Fields
		aliasTime(f, "published_at", "create_time", "createTime", "gmtCreate")
		aliasTime(f, "updated_at", "update_time", "updateTime", "gmtModified")
		if f["published_at_raw"] == nil || f["updated_at_raw"] == nil {
			t.Fatal(f)
		}
	}
	for _, fields := range []string{`"create_time":{"url":"private"}`, `"gmtCreate":["2026-10-01"]`} {
		_, _, _, _, err := parseTopics([]byte(`{"status":true,"datas":[{"uuid":"t","content":"Body",` + fields + `}],"poff":{"lastPage":1}}`))
		if !errors.Is(err, contract.ErrParse) {
			t.Fatal("non-scalar metadata accepted", err)
		}
	}
}

func TestUnlabelledAssignmentDatesAndDescriptionDatesAreNotDeadlines(t *testing.T) {
	body := `<div>1/1</div><li data="/work/task?courseId=c&amp;classId=k&amp;workId=a" aria-label="Date 2026-11-09;未交"><p class=overHidden2>截止时间2026-11-09</p><p>作业说明：比较发布时间：2026-11-01 与截止时间：2026-11-09</p></li>`
	rows, _, err := assignments([]byte(body), course{ID: "c", Class: "k"})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	if rows[0].Fields["deadline_raw"] != nil || rows[0].Fields["published_at_raw"] != nil {
		t.Fatal("inferred label from title/description", rows[0].Fields)
	}
}
