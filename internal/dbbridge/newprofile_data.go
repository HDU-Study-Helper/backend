package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

// dataDataset joins only owner-scoped business tables; all SQL identifiers and
// expressions originate here. No model input is used as an SQL identifier.
type dataDataset struct {
	from, order, key string
	where            []string
	fields           map[string]string
	times            map[string]bool
	names, search    []string
}

func profileDatasets() map[string]dataDataset {
	schedules := dataDataset{
		from: "schedules a JOIN users u ON u.id=a.user_id", order: "a.start_time,a.id", key: "a.id",
		where: []string{"a.user_id=$1", "a.active", "NOT u.disabled"}, fields: map[string]string{}, times: map[string]bool{},
		names:  []string{"id", "title", "details", "source", "academic_year", "term", "teaching_week", "start_time", "end_time", "event_status", "series_key", "origin_key", "weeks_raw", "week_pattern", "created_at", "updated_at"},
		search: []string{"title", "details", "weeks_raw", "week_pattern"},
	}
	for _, name := range []string{"id", "title", "details", "source", "event_status", "series_key", "origin_key"} {
		schedules.fields[name] = "a." + name
	}
	for _, name := range []string{"academic_year", "term", "teaching_week"} {
		schedules.fields[name] = "a." + name + "::text"
	}
	for _, name := range []string{"start_time", "end_time", "created_at", "updated_at"} {
		schedules.fields[name] = "a." + name
		schedules.times[name] = true
	}
	for _, name := range []string{"weeks_raw", "week_pattern"} {
		schedules.fields[name] = "COALESCE(a.mod->>'" + name + "','')"
	}
	messages := dataDataset{
		from:  "dws_messages a JOIN dws_connections c ON c.user_id=a.user_id AND c.id=a.connection_id JOIN dws_groups g ON g.user_id=a.user_id AND g.connection_id=a.connection_id AND g.id=a.group_id JOIN users u ON u.id=a.user_id",
		order: "a.id", key: "a.id::text", where: []string{"a.user_id=$1", "c.enabled", "c.state='active'", "NOT u.disabled"}, fields: map[string]string{}, times: map[string]bool{},
		names:  []string{"id", "connection_id", "source", "group_id", "group_name", "external_id", "source_version", "text", "sender", "sender_id", "message_type", "create_time_raw", "update_time_raw", "reply_id", "thread_id", "observed_at", "quoted_text", "forwarded_text", "attachment_names"},
		search: []string{"text", "group_name", "sender", "quoted_text", "forwarded_text", "attachment_names"},
	}
	for _, name := range []string{"connection_id", "group_id", "external_id", "source_version"} {
		messages.fields[name] = "a." + name
	}
	messages.fields["id"] = "a.id::text"
	messages.fields["source"] = "'dws'::text"
	messages.fields["group_name"] = "g.name"
	doc := "convert_from(a.content,'UTF8')::jsonb"
	for name, key := range map[string]string{"text": "text", "sender": "sender", "sender_id": "senderId", "message_type": "messageType", "create_time_raw": "createTime", "update_time_raw": "updateTime", "reply_id": "replyId", "thread_id": "threadId"} {
		messages.fields[name] = "CASE WHEN jsonb_typeof((" + doc + ")->'" + key + "') IN ('string','number','boolean') THEN COALESCE((" + doc + ")->>'" + key + "','') ELSE '' END"
	}
	for name, path := range map[string]string{"quoted_text": "quotedMessage", "forwarded_text": "forwarded"} {
		// Extract only text leaves from already archived message content;
		// transport arguments, IDs and arbitrary nested objects stay private.
		messages.fields[name] = "COALESCE((SELECT string_agg(v.value #>> '{}',' ') FROM jsonb_path_query(((" + doc + ")->'" + path + "'),'$.**.text ? (@.type() == \"string\")') v(value)),'')"
	}
	messages.fields["attachment_names"] = "CASE WHEN jsonb_typeof((" + doc + ")->'resourceRefs')='array' THEN COALESCE((SELECT string_agg(p.value->>'name',' ') FROM jsonb_array_elements((" + doc + ")->'resourceRefs') p(value) WHERE jsonb_typeof(p.value->'name')='string'),'') ELSE '' END"
	messages.fields["observed_at"] = "a.observed_at"
	messages.times["observed_at"] = true
	attachments := dataDataset{
		from:  "dws_attachments b JOIN dws_messages a ON a.user_id=b.user_id AND a.connection_id=b.connection_id AND a.id=b.message_id JOIN dws_connections c ON c.user_id=a.user_id AND c.id=a.connection_id JOIN dws_groups g ON g.user_id=a.user_id AND g.connection_id=a.connection_id AND g.id=a.group_id JOIN users u ON u.id=a.user_id",
		order: "a.id,b.id", key: "jsonb_build_array(a.id::text,b.id)::text", where: messages.where,
		fields: map[string]string{"id": "b.id", "message_id": "a.id::text", "connection_id": "a.connection_id", "source": "'dws'::text", "group_id": "a.group_id", "group_name": "g.name", "name": "b.name", "media_type": "b.media_type", "sha256": "b.sha256", "size_bytes": "octet_length(b.data)::text"}, times: map[string]bool{},
		names: []string{"id", "message_id", "connection_id", "source", "group_id", "group_name", "name", "media_type", "sha256", "size_bytes"}, search: []string{"name", "group_name"},
	}
	return map[string]dataDataset{"schedules": schedules, "dws_messages": messages, "dws_attachments": attachments}
}

func profileDataCatalog() []f.Dataset {
	sets := profileDatasets()
	out := []f.Dataset{}
	for _, name := range []string{"schedules", "dws_messages", "dws_attachments"} {
		set := sets[name]
		fields := []f.Field{}
		for _, key := range set.names {
			typ := "string"
			if set.times[key] {
				typ = "timestamp"
			}
			fields = append(fields, f.Field{Name: key, Type: typ})
		}
		out = append(out, f.Dataset{Name: name, Fields: fields})
	}
	return out
}

func agentDatasets() map[string]dataDataset {
	sets := profileDatasets()
	sets["platform_records"] = dataDataset{from: "platform_records r JOIN platform_connections c ON c.user_id=r.user_id AND c.id=r.connection_id JOIN users u ON u.id=r.user_id", order: "r.id", key: "r.id::text", where: []string{"r.user_id=$1", "c.enabled", "c.state='active'", "NOT u.disabled"}, fields: dataFields, times: map[string]bool{}, names: dataFieldOrder, search: []string{"title", "text", "course_name", "teacher", "teacher_raw", "teacher_name", "location", "location_raw", "class_name", "schedule_raw", "metadata"}}
	for name, set := range academicDatasets() {
		search := []string{"course_name", "teacher", "description", "details"}
		if name == "academic_items" {
			search = []string{"title", "text", "course_name", "author", "path", "metadata"}
		}
		sets[name] = dataDataset{from: set.from + " JOIN platform_connections c ON c.user_id=a.user_id AND c.id=a.connection_id JOIN users u ON u.id=a.user_id", order: set.order, key: "a.id", where: []string{"a.user_id=$1", "c.enabled", "c.state='active'", "NOT u.disabled"}, fields: set.fields, times: set.times, names: set.names, search: search}
	}
	return sets
}

func queryProjection(set dataDataset, fields []string) ([]string, error) {
	cols := []string{}
	seen := map[string]bool{}
	for _, name := range fields {
		expr, ok := set.fields[name]
		if !ok || seen[name] {
			return nil, f.Fail("PERMISSION_DENIED", "query field is unavailable or duplicated: "+name+"; choose from catalog: "+strings.Join(set.names, ","))
		}
		seen[name] = true
		cols = append(cols, expr+" AS "+pgx.Identifier{name}.Sanitize())
	}
	return cols, nil
}

func queryFilters(set dataDataset, filters []f.Filter, args *[]any) ([]string, error) {
	where := append([]string{}, set.where...)
	for _, filter := range filters {
		expr, ok := set.fields[filter.Field]
		if !ok {
			return nil, f.Fail("PERMISSION_DENIED", "filter field is unavailable: "+filter.Field+"; choose from catalog: "+strings.Join(set.names, ","))
		}
		value, ok := filter.Value.(string)
		if !ok || len(value) > 4096 {
			return nil, f.Fail("INVALID_ARGUMENT", "dataset filters require bounded string values")
		}
		op := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[filter.Operator]
		if filter.Operator != "contains" && op == "" {
			return nil, f.Fail("INVALID_ARGUMENT", "unsupported filter operator")
		}
		if set.times[filter.Field] {
			if filter.Operator == "contains" {
				return nil, f.Fail("INVALID_ARGUMENT", "timestamp filters require comparison")
			}
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return nil, f.Fail("INVALID_ARGUMENT", "timestamp filters require RFC3339 with timezone")
			}
		}
		*args = append(*args, value)
		param := fmt.Sprintf("$%d", len(*args))
		if set.times[filter.Field] {
			param += "::timestamptz"
		}
		if filter.Operator == "contains" {
			where = append(where, "strpos("+expr+","+param+")>0")
		} else {
			where = append(where, expr+op+param)
		}
	}
	return where, nil
}

func scanDataRow(values []any, fields []string, times map[string]bool) map[string]any {
	row := map[string]any{}
	for i, name := range fields {
		value := values[i]
		if times[name] {
			if at, ok := value.(time.Time); ok {
				value = at.UTC().Format(time.RFC3339Nano)
			} else {
				value = ""
			}
		}
		row[name] = value
	}
	return row
}

func (r *agentData) queryDataset(ctx context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	result := f.QueryResult{Rows: []map[string]any{}}
	set, ok := agentDatasets()[q.Dataset]
	if !ok || s.OwnerID == "" {
		return result, f.Fail("PERMISSION_DENIED", "dataset is outside authorized scope")
	}
	if len(q.Fields) < 1 || len(q.Fields) > 32 || q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 10000 || len(q.Filters) > 20 {
		return result, f.Fail("INVALID_ARGUMENT", "query exceeds authorized dataset constraints")
	}
	cols, err := queryProjection(set, q.Fields)
	if err != nil {
		return result, err
	}
	args := []any{s.OwnerID}
	where, err := queryFilters(set, q.Filters, &args)
	if err != nil {
		return result, err
	}
	args = append(args, q.Limit+1, q.Offset)
	sql := "SELECT " + strings.Join(cols, ",") + " FROM " + set.from + " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + set.order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SET TRANSACTION READ ONLY"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SET LOCAL statement_timeout='5s'"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SET LOCAL jit=off"); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, sql, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			values, e := rows.Values()
			if e != nil {
				return e
			}
			result.Rows = append(result.Rows, scanDataRow(values, q.Fields, set.times))
		}
		return rows.Err()
	})
	if err != nil {
		return f.QueryResult{Rows: []map[string]any{}}, agentError(err)
	}
	if len(result.Rows) > q.Limit {
		result.Rows = result.Rows[:q.Limit]
		result.HasMore = true
		if next := q.Offset + q.Limit; next <= 10000 {
			result.NextOffset = &next
		}
	}
	// Return a smaller truthful page when full metadata/text exceeds model tool
	// budgets. The next offset advances only over rows actually returned.
	const pageBytes = 72 << 10
	used, keep := 0, 0
	for i, row := range result.Rows {
		raw, e := json.Marshal(row)
		if e != nil {
			return f.QueryResult{}, f.Fail("INTERNAL_ERROR", "cannot encode query row")
		}
		if used+len(raw) > pageBytes && i > 0 {
			break
		}
		if len(raw) > pageBytes {
			return f.QueryResult{}, f.Fail("INVALID_ARGUMENT", "one query row exceeds response budget; select fewer fields, especially text/metadata, and query them separately")
		}
		used += len(raw)
		keep = i + 1
	}
	if keep < len(result.Rows) {
		result.Rows = result.Rows[:keep]
		result.HasMore = true
		next := q.Offset + keep
		if next <= 10000 {
			result.NextOffset = &next
		} else {
			result.NextOffset = nil
		}
	}
	return result, nil
}
