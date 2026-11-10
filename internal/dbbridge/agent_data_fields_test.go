package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func TestAgentDataBusinessFieldAllowlistMatchesCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range dataFieldOrder {
		if seen[name] || dataFields[name] == "" {
			t.Fatalf("invalid catalog field %q", name)
		}
		seen[name] = true
	}
	if len(seen) != len(dataFields) {
		t.Fatal("query fields missing from catalog")
	}
	for _, name := range []string{"fields", "content", "raw", "session", "token", "cookie", "api_key", "state", "strategies"} {
		if _, ok := dataFields[name]; ok {
			t.Fatalf("unsafe raw field exposed: %s", name)
		}
	}
	if strings.Contains(dataFields["deadline"], "endTime") || strings.Contains(dataFields["deadline"], "strategies") {
		t.Fatal("availability must not become a deadline")
	}
	if strings.Contains(dataFields["published_at"], "createTime") || strings.Contains(dataFields["published_at"], "observed_at") {
		t.Fatal("source creation or observation must not become publication")
	}
}

// Requires a dedicated TEST_DATABASE_URL. testDB creates and cleans an isolated
// schema; the default suite skips this test rather than querying live accounts.
func TestAgentDataBusinessFieldsPreserveSourceValuesAndIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "business-fields", model.RoleUser)
	other := fixtureUser(t, db, "fields-other", model.RoleUser)
	for _, u := range []string{owner.ID, other.ID} {
		for _, source := range []string{"chaoxing", "mooc", "ucampus"} {
			_, err := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,$2,$2,'active')`, u, source)
			must(t, err)
		}
	}
	insert := func(who, source, kind, external, body string) string {
		t.Helper()
		var id string
		must(t, db.pool.QueryRow(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,$2,$2,$3,$4,'parent','hash',$5,now()) RETURNING id::text`, who, source, kind, external, body).Scan(&id))
		return id
	}
	query := func(who, id string, fields []string) []map[string]any {
		t.Helper()
		result, err := db.AgentData().Query(ctx, f.Scope{OwnerID: who}, f.Query{Dataset: "platform_records", Fields: fields, Filters: []f.Filter{{Field: "id", Operator: "eq", Value: id}}, Limit: 1})
		must(t, err)
		return result.Rows
	}
	cx := insert(owner.ID, "chaoxing", "assignment", "cx-work", `{"title":"实验一","content":"完整任务正文","fields":{"course_id":"cx-course","class_id":"cx-class","status":"待提交","deadline":"2027-01-02 18:00","published_at":"2026-09-03T09:00:00+08:00","cookie":"private-value","state":{"secret":"private-value"}}}`)
	mooc := insert(owner.ID, "mooc", "assignment", "mooc-work", `{"title":"单元测验","fields":{"courseId":"m-course","termId":"m-term","statusText":"未提交","submitStatus":0,"finishStatus":false,"submitEndTime":1790000000000,"startTime":1789000000000,"endTime":1791000000000,"task_type":"quiz"}}`)
	uc := insert(owner.ID, "ucampus", "assignment", "uc-work", `{"fields":{"course_id":"u-course","class_id":"u-class","strategies":{"start_time":"2026-09-02","end_time":"2026-10-02","private":"private-value"}}}`)
	material := insert(owner.ID, "chaoxing", "material", "file", `{"fields":{"file_type":"pdf","size_raw":"2MB","path":["资料","实验一.pdf"],"is_folder":false,"time_raw":"昨日","token":"private-value"}}`)
	exam := insert(owner.ID, "chaoxing", "exam", "exam", `{"fields":{"time_raw":"2026-10-01 08:00~10:00","submission_status":"未提交","availability_status":"未开始"}}`)
	malformed := insert(owner.ID, "mooc", "assignment", "malformed", `{"fields":{"status":{"cookie":"private-value"},"deadline":["private-value"],"path":[{"token":"private-value"}],"endTime":"2027-01-01","createTime":"2026-09-02"}}`)
	course := insert(owner.ID, "mooc", "course", "course", `{"title":"大学物理","fields":{"courseId":"m-course","termId":"m-term"}}`)
	rows := query(owner.ID, cx, []string{"id", "text", "course_id", "class_id", "course_name", "status", "source_status", "deadline", "published_at"})
	want := map[string]string{"id": cx, "text": "完整任务正文", "course_id": "cx-course", "class_id": "cx-class", "course_name": "", "status": "待提交", "source_status": "待提交", "deadline": "2027-01-02 18:00", "published_at": "2026-09-03T09:00:00+08:00"}
	for key, value := range want {
		if rows[0][key] != value {
			t.Fatalf("CX %s: got %#v want %q", key, rows[0][key], value)
		}
	}
	rows = query(owner.ID, mooc, []string{"course_id", "term_id", "course_name", "status", "submission_status", "finish_status", "deadline", "start_time", "end_time", "published_at", "task_type"})
	want = map[string]string{"course_id": "m-course", "term_id": "m-term", "course_name": "", "status": "未提交", "submission_status": "0", "finish_status": "false", "deadline": "1790000000000", "start_time": "1789000000000", "end_time": "1791000000000", "published_at": "", "task_type": "quiz"}
	for key, value := range want {
		if rows[0][key] != value {
			t.Fatalf("MOOC %s: got %#v want %q", key, rows[0][key], value)
		}
	}
	rows = query(owner.ID, uc, []string{"course_id", "class_id", "start_time", "end_time", "deadline"})
	if rows[0]["start_time"] != "2026-09-02" || rows[0]["end_time"] != "2026-10-02" || rows[0]["deadline"] != "" {
		t.Fatal("U-campus availability semantics changed", rows)
	}
	rows = query(owner.ID, material, []string{"file_type", "size_raw", "path", "is_folder", "source_time", "published_at"})
	if rows[0]["file_type"] != "pdf" || rows[0]["is_folder"] != "false" || rows[0]["source_time"] != "昨日" || rows[0]["published_at"] != "" {
		t.Fatal(rows)
	}
	var path []string
	if json.Unmarshal([]byte(rows[0]["path"].(string)), &path) != nil || len(path) != 2 || path[1] != "实验一.pdf" {
		t.Fatal("path lost", rows)
	}
	rows = query(owner.ID, malformed, []string{"status", "deadline", "path", "end_time", "source_time", "published_at"})
	for _, key := range []string{"status", "deadline", "path", "published_at"} {
		if rows[0][key] != "" {
			t.Fatalf("malformed/private field escaped via %s: %+v", key, rows)
		}
	}
	if rows[0]["end_time"] != "2027-01-01" || rows[0]["source_time"] != "2026-09-02" {
		t.Fatal(rows)
	}
	rows = query(owner.ID, exam, []string{"status", "source_status", "submission_status", "availability_status", "time_raw", "start_time", "end_time", "deadline"})
	if rows[0]["status"] != "未提交" || rows[0]["source_status"] != "未提交" || rows[0]["availability_status"] != "未开始" || rows[0]["time_raw"] != "2026-10-01 08:00~10:00" || rows[0]["start_time"] != "" || rows[0]["end_time"] != "" || rows[0]["deadline"] != "" {
		t.Fatal("exam status or raw time altered", rows)
	}
	rows = query(owner.ID, course, []string{"course_name"})
	if rows[0]["course_name"] != "大学物理" {
		t.Fatal(rows)
	}
	for _, id := range []string{cx, mooc, uc, material, exam, malformed, course} {
		for start := 0; start < len(dataFieldOrder); start += 32 {
			end := start + 32
			if end > len(dataFieldOrder) {
				end = len(dataFieldOrder)
			}
			rows = query(owner.ID, id, dataFieldOrder[start:end])
			if strings.Contains(fmt.Sprint(rows), "private-value") {
				t.Fatal("private metadata leaked", rows)
			}
		}
	}
	if rows = query(other.ID, cx, []string{"course_id", "status", "deadline"}); len(rows) != 0 {
		t.Fatal("business fields bypassed owner isolation")
	}
	_, err := db.pool.Exec(ctx, `UPDATE platform_connections SET enabled=false WHERE user_id=$1 AND id='chaoxing'`, owner.ID)
	must(t, err)
	if rows = query(owner.ID, cx, []string{"status"}); len(rows) != 0 {
		t.Fatal("disabled connection exposed source values")
	}
	_, err = db.pool.Exec(ctx, `UPDATE platform_connections SET state='expired' WHERE user_id=$1 AND id='mooc'`, owner.ID)
	must(t, err)
	if rows = query(owner.ID, mooc, []string{"status"}); len(rows) != 0 {
		t.Fatal("expired connection exposed source values")
	}
}
