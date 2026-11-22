package dbbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func TestKeywordSearchPlanLiteralORAndCursorBinding(t *testing.T) {
	request := f.KeywordSearchRequest{Keywords: []string{" MATH ", "报告", "math", "100%_quote'"}, Datasets: []string{"schedules", "dws_messages"}, Limit: 100}
	keywords, datasets, cursor, err := keywordSearchPlan("owner", request)
	must(t, err)
	if len(keywords) != 3 || fmt.Sprint(datasets) != "[schedules dws_messages]" {
		t.Fatal(keywords, datasets)
	}
	args := []any{"owner"}
	predicate := keywordPredicate(agentDatasets()["schedules"], keywords, &args)
	if !strings.Contains(predicate, "CROSS JOIN unnest") || strings.Contains(predicate, "报告") || strings.Contains(predicate, "100%") || strings.Contains(predicate, "LIKE") || len(args) != 4 {
		t.Fatal("keywords must be parameterized literal OR", predicate, args)
	}
	cursor.Key = "schedule-id"
	raw, _ := json.Marshal(cursor)
	request.Cursor = base64.RawURLEncoding.EncodeToString(raw)
	_, _, _, err = keywordSearchPlan("owner", request)
	must(t, err)
	if _, _, _, err = keywordSearchPlan("other", request); err == nil {
		t.Fatal("cross owner cursor accepted")
	}
	request.Keywords = []string{"different"}
	if _, _, _, err = keywordSearchPlan("owner", request); err == nil {
		t.Fatal("changed keyword cursor accepted")
	}
	for _, bad := range []f.KeywordSearchRequest{{Keywords: []string{" "}, Limit: 1}, {Keywords: []string{"ok"}, Datasets: []string{"users"}, Limit: 1}, {Keywords: []string{"ok"}, Limit: 101}, {Keywords: []string{strings.Repeat("汉", 257)}, Limit: 1}} {
		if _, _, _, err = keywordSearchPlan("owner", bad); err == nil {
			t.Fatal("invalid request accepted", bad)
		}
	}
}

func TestDataQueryCatalogSafetyAndANDCompatibility(t *testing.T) {
	for name, set := range agentDatasets() {
		seen := map[string]bool{}
		for _, field := range set.names {
			if seen[field] || set.fields[field] == "" {
				t.Fatalf("%s catalog mismatch %s", name, field)
			}
			seen[field] = true
		}
		for _, field := range set.search {
			if set.fields[field] == "" {
				t.Fatalf("%s unknown search field %s", name, field)
			}
		}
		for _, unsafe := range []string{"user_id", "workspace", "token", "cookie", "password", "data", "resource_refs", "download_url", "raw", "mod"} {
			if _, ok := set.fields[unsafe]; ok {
				t.Fatalf("%s unsafe field %s", name, unsafe)
			}
		}
		if !strings.Contains(strings.Join(set.where, " "), "NOT u.disabled") {
			t.Fatal("disabled user omitted", name)
		}
	}
	for _, name := range []string{"dws_messages", "dws_attachments"} {
		set := agentDatasets()[name]
		for _, required := range []string{"g.user_id=a.user_id", "g.connection_id=a.connection_id", "g.id=a.group_id", "c.user_id=a.user_id", "c.id=a.connection_id"} {
			if !strings.Contains(set.from, required) {
				t.Fatal(name, "missing authorization", required)
			}
		}
	}
	set := agentDatasets()["schedules"]
	args := []any{"owner"}
	where, err := queryFilters(set, []f.Filter{{Field: "title", Operator: "contains", Value: "数学"}, {Field: "source", Operator: "eq", Value: "TIMETABLE"}}, &args)
	must(t, err)
	if len(where) != len(set.where)+2 || strings.Contains(strings.Join(where, " AND "), " OR ") || len(args) != 3 {
		t.Fatal("existing AND filters changed", where, args)
	}
	if _, err := queryFilters(set, []f.Filter{{Field: "start_time", Operator: "gte", Value: "2026-01-01"}}, &args); err == nil {
		t.Fatal("ambiguous timestamp accepted")
	}
}

func TestDataQueryUnavailableFieldsAreRecoverableArguments(t *testing.T) {
	for _, dataset := range []string{"dws_messages", "academic_items", "platform_records"} {
		set := agentDatasets()[dataset]
		for _, field := range []string{"labels", "session", "password", "unknown_field"} {
			_, err := queryProjection(set, []string{field})
			code(t, err, "INVALID_ARGUMENT")
			args := []any{"owner"}
			_, err = queryFilters(set, []f.Filter{{Field: field, Operator: "eq", Value: "x"}}, &args)
			code(t, err, "INVALID_ARGUMENT")
			if len(args) != 1 {
				t.Fatal("unavailable field reached filter argument construction")
			}
		}
		_, err := queryProjection(set, []string{set.names[0], set.names[0]})
		code(t, err, "INVALID_ARGUMENT")
	}
	data := &agentData{}
	for _, q := range []f.Query{{Dataset: "agent_memories", Fields: []string{"content"}, Limit: 1}, {Dataset: "dws_messages", Fields: []string{"labels"}, Limit: 1}} {
		scope := f.Scope{OwnerID: "owner"}
		if q.Dataset == "dws_messages" {
			scope.OwnerID = ""
		}
		_, err := data.queryDataset(context.Background(), scope, q)
		code(t, err, "PERMISSION_DENIED")
	}
}

func TestKeywordSearchBoundedSummariesPreserveCursor(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "bounded-search", model.RoleUser)
	_, err := db.pool.Exec(ctx, `INSERT INTO schedules(id,user_id,academic_year,term,teaching_week,start_time,end_time,title,details,source) SELECT 'large-'||n::text,$1,2026,1,1,'2026-11-10T00:00:00Z'::timestamptz+n*interval '1 day','2026-11-10T01:00:00Z'::timestamptz+n*interval '1 day','Math '||repeat('汉',2000),repeat('文',4000),'USER' FROM generate_series(1,100) n`, owner.ID)
	must(t, err)
	request := f.KeywordSearchRequest{Keywords: []string{"Math"}, Datasets: []string{"schedules"}, Limit: 100}
	seen := map[string]bool{}
	service := db.AgentData().(f.KeywordSearchService)
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("summary cursor did not complete")
		}
		result, err := service.SearchKeywords(ctx, f.Scope{OwnerID: owner.ID}, request)
		must(t, err)
		raw, err := json.Marshal(result)
		must(t, err)
		if len(raw) > 64*1024 {
			t.Fatal("search response budget exceeded", len(raw))
		}
		for _, hit := range result.Hits {
			id := hit.Row["id"].(string)
			if seen[id] {
				t.Fatal("summary budget replayed hit", id)
			}
			seen[id] = true
			if len(hit.Row["title"].(string)) > 1024 || hit.Row["_truncated_fields"] == nil {
				t.Fatal("missing summary truncation", hit)
			}
		}
		if !result.HasMore {
			break
		}
		if result.NextCursor == "" || result.NextCursor == request.Cursor {
			t.Fatal("budget cursor not advancing")
		}
		request.Cursor = result.NextCursor
	}
	if len(seen) != 100 {
		t.Fatal("response budget skipped hits", len(seen))
	}
}

func TestNewProfileRawAndNestedMetadataWhitelists(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "raw-keyword-meta", model.RoleUser)
	_, err := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,'jw','jwxt','active'),($1,'mooc','mooc','active')`, owner.ID)
	must(t, err)
	_, err = db.pool.Exec(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,'jw','jwxt','course','math','term','hash',$2,now()),($1,'mooc','mooc','assignment','work','term','hash',$3,now())`, owner.ID,
		`{"title":"数学","fields":{"teacher":"张老师","location":"A101","total_hours_raw":"64","weekly_hours_raw":"4","schedule_raw":"周一第1-2节","teacher_raw":{"token":"private-secret"}}}`,
		`{"title":"Task","fields":{"question_count":12,"full_score":"100","answer_time_raw":"60分钟","cookie":"private-secret","header_status":{"token":"private-secret"},"native_progress_fields":{"submitStatus":0,"learnTime":120,"token":"private-secret"},"current_user_participation_fields":{"replyCount":2,"cookie":"private-secret"},"current_user_state":{"hasFollowed":true,"hasVoteUp":false,"hasVoteDown":false,"token":"private-secret"},"assessment_time_fields":{"node":{"releaseTime":1790000000000,"token":"private-secret"},"test":{"examEndTime":"2026-12-01","cookie":"private-secret"}},"path_ids":[{"token":"private-secret"}]}}`)
	must(t, err)
	scope := f.Scope{OwnerID: owner.ID}
	rows, err := db.AgentData().Query(ctx, scope, f.Query{Dataset: "platform_records", Fields: []string{"teacher", "teacher_raw", "location", "total_hours_raw", "weekly_hours_raw", "schedule_raw"}, Filters: []f.Filter{{Field: "external_id", Operator: "eq", Value: "math"}}, Limit: 1})
	must(t, err)
	if len(rows.Rows) != 1 || rows.Rows[0]["teacher"] != "张老师" || rows.Rows[0]["teacher_raw"] != "" || rows.Rows[0]["total_hours_raw"] != "64" || rows.Rows[0]["weekly_hours_raw"] != "4" || rows.Rows[0]["schedule_raw"] != "周一第1-2节" {
		t.Fatal("raw JWXT fields changed", rows)
	}
	rows, err = db.AgentData().Query(ctx, scope, f.Query{Dataset: "platform_records", Fields: []string{"metadata"}, Filters: []f.Filter{{Field: "external_id", Operator: "eq", Value: "work"}}, Limit: 1})
	must(t, err)
	var meta map[string]any
	must(t, json.Unmarshal([]byte(rows.Rows[0]["metadata"].(string)), &meta))
	if strings.Contains(fmt.Sprint(meta), "private-secret") || meta["question_count"] != float64(12) || meta["answer_time_raw"] != "60分钟" || meta["current_user_state"].(map[string]any)["hasFollowed"] != true || meta["assessment_time_fields"].(map[string]any)["test"].(map[string]any)["examEndTime"] != "2026-12-01" {
		t.Fatal("safe metadata omitted or unsafe metadata leaked", meta)
	}
	service := db.AgentData().(f.KeywordSearchService)
	result, err := service.SearchKeywords(ctx, scope, f.KeywordSearchRequest{Keywords: []string{"TEST"}, Datasets: []string{"platform_records"}, Limit: 100})
	must(t, err)
	if len(result.Hits) != 0 {
		t.Fatal("metadata schema keys falsely match keywords", result)
	}
	result, err = service.SearchKeywords(ctx, scope, f.KeywordSearchRequest{Keywords: []string{"60分钟", "张老师"}, Datasets: []string{"platform_records"}, Limit: 1})
	must(t, err)
	if len(result.Hits) != 1 || !result.HasMore {
		t.Fatal("safe metadata/source fields not searchable", result)
	}
}

func TestNewProfileDatasetsKeywordPagingAndAuthorization(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	owner := fixtureUser(t, db, "profile-search", model.RoleUser)
	other := fixtureUser(t, db, "profile-other", model.RoleUser)
	for _, who := range []string{owner.ID, other.ID} {
		_, err := db.pool.Exec(ctx, `INSERT INTO dws_connections(user_id,id,profile,mode,state) VALUES($1,'dws','fixture','personal','active');`, who)
		must(t, err)
		_, err = db.pool.Exec(ctx, `INSERT INTO dws_groups(user_id,connection_id,id,name) VALUES($1,'dws','allowed','数学通知');`, who)
		must(t, err)
		for i := 0; i < 3; i++ {
			raw := fmt.Sprintf(`{"text":"Math report %d","sender":"老师","createTime":1790000000000,"resourceRefs":[{"name":"报告.docx","download":{"arguments":{"token":"private-secret"}}}],"quotedMessage":{"text":"引用报告","downloadUrl":"private-secret"},"forwarded":{"messages":[{"text":"转发报告","token":"private-secret"}]},"cookie":"private-secret"}`, i)
			_, err = db.pool.Exec(ctx, `INSERT INTO dws_messages(user_id,connection_id,group_id,external_id,source_version,content_hash,content,observed_at) VALUES($1,'dws','allowed',$2,'v','hash',$3,now())`, who, fmt.Sprint(i), []byte(raw))
			must(t, err)
		}
		_, err = db.pool.Exec(ctx, `INSERT INTO dws_messages(user_id,connection_id,group_id,external_id,source_version,content_hash,content,observed_at) VALUES($1,'dws','revoked','hidden','v','hash',$2,now())`, who, []byte(`{"text":"Math private-secret"}`))
		must(t, err)
		_, err = db.pool.Exec(ctx, `INSERT INTO schedules(id,user_id,academic_year,term,teaching_week,start_time,end_time,title,details,source) SELECT $1||n::text,$1,2026,1,1,'2026-11-10T00:00:00Z'::timestamptz+n*interval '1 day','2026-11-10T01:00:00Z'::timestamptz+n*interval '1 day','数学',CASE WHEN n=1 THEN '报告' ELSE '课程' END,'USER' FROM generate_series(1,3) n`, who)
		must(t, err)
	}
	scope := f.Scope{OwnerID: owner.ID}
	first, err := db.AgentData().Query(ctx, scope, f.Query{Dataset: "schedules", Fields: []string{"id", "title", "start_time"}, Limit: 2})
	must(t, err)
	if len(first.Rows) != 2 || !first.HasMore || first.NextOffset == nil || *first.NextOffset != 2 {
		t.Fatal("missing page metadata", first)
	}
	last, err := db.AgentData().Query(ctx, scope, f.Query{Dataset: "schedules", Fields: []string{"id"}, Limit: 2, Offset: 2})
	must(t, err)
	if len(last.Rows) != 1 || last.HasMore || last.NextOffset != nil {
		t.Fatal("incorrect final page", last)
	}
	search := db.AgentData().(f.KeywordSearchService)
	request := f.KeywordSearchRequest{Keywords: []string{"mAtH", "数学", "报告"}, Datasets: []string{"schedules", "dws_messages"}, Limit: 2}
	seen := map[string]bool{}
	pages := 0
	for {
		result, err := search.SearchKeywords(ctx, scope, request)
		must(t, err)
		pages++
		if len(result.Hits) > 2 || strings.Contains(fmt.Sprint(result), "private-secret") {
			t.Fatal("budget or private metadata leak", result)
		}
		for _, coverage := range result.Coverage {
			if coverage.Matched != 3 {
				t.Fatal("coverage crossed owner/group boundary", result)
			}
		}
		for _, hit := range result.Hits {
			key := hit.Dataset + ":" + fmt.Sprint(hit.Row["id"])
			if seen[key] {
				t.Fatal("duplicate across keyword/page union", key)
			}
			seen[key] = true
		}
		if !result.HasMore {
			for _, coverage := range result.Coverage {
				if !coverage.Complete {
					t.Fatal("final incomplete", result)
				}
			}
			break
		}
		if result.NextCursor == "" || result.NextCursor == request.Cursor || pages > 5 {
			t.Fatal("cursor failed to advance", result)
		}
		request.Cursor = result.NextCursor
	}
	if len(seen) != 6 || pages != 3 {
		t.Fatal("retrieval truncated or merged names", seen, pages)
	}
	quoted, err := db.AgentData().Query(ctx, scope, f.Query{Dataset: "dws_messages", Fields: []string{"quoted_text", "forwarded_text", "attachment_names"}, Limit: 1})
	must(t, err)
	if len(quoted.Rows) != 1 || !strings.Contains(quoted.Rows[0]["quoted_text"].(string), "引用报告") || !strings.Contains(quoted.Rows[0]["forwarded_text"].(string), "转发报告") || quoted.Rows[0]["attachment_names"] != "报告.docx" || strings.Contains(fmt.Sprint(quoted), "private-secret") {
		t.Fatal("message context projection lost or leaked", quoted)
	}
	_, err = db.pool.Exec(ctx, `UPDATE dws_connections SET enabled=false WHERE user_id=$1`, owner.ID)
	must(t, err)
	disabled, err := search.SearchKeywords(ctx, scope, f.KeywordSearchRequest{Keywords: []string{"math"}, Datasets: []string{"dws_messages"}, Limit: 100})
	must(t, err)
	if len(disabled.Hits) != 0 {
		t.Fatal("disabled DWS connection remained searchable")
	}
	_, err = db.pool.Exec(ctx, `UPDATE dws_connections SET enabled=true WHERE user_id=$1`, owner.ID)
	must(t, err)
	_, err = db.pool.Exec(ctx, `DELETE FROM dws_groups WHERE user_id=$1`, owner.ID)
	must(t, err)
	result, err := search.SearchKeywords(ctx, scope, f.KeywordSearchRequest{Keywords: []string{"math"}, Datasets: []string{"dws_messages", "dws_attachments"}, Limit: 100})
	must(t, err)
	if len(result.Hits) != 0 {
		t.Fatal("revoked group remained readable", result)
	}
	_, err = db.pool.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, owner.ID)
	must(t, err)
	result, err = search.SearchKeywords(ctx, scope, f.KeywordSearchRequest{Keywords: []string{"数学"}, Limit: 100})
	must(t, err)
	if len(result.Hits) != 0 {
		t.Fatal("disabled owner remained readable", result)
	}
}
