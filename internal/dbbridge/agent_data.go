package dbbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

// AgentData exposes a small readonly projection of existing per-owner platform records.
// Raw content, private memories, sessions, credentials and arbitrary SQL are never exposed.
func (d *DB) AgentData() f.DataService { return &agentData{db: d} }

type agentData struct{ db *DB }

var dataFields = map[string]string{"id": "r.id::text", "connection_id": "r.connection_id", "source": "r.source", "kind": "r.kind", "external_id": "r.external_id", "parent_id": "r.parent_id", "title": "COALESCE(r.content->>'title','')", "text": "COALESCE(r.content->>'text','')"}

func (r *agentData) Catalog(ctx context.Context, s f.Scope) ([]f.Dataset, error) {
	var owner string
	if err := r.db.pool.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled`, s.OwnerID).Scan(&owner); err != nil {
		return nil, agentError(err)
	}
	fields := []f.Field{}
	for _, name := range []string{"id", "connection_id", "source", "kind", "external_id", "parent_id", "title", "text"} {
		fields = append(fields, f.Field{Name: name, Type: "string"})
	}
	return []f.Dataset{{Name: "platform_records", Fields: fields}}, nil
}
func (r *agentData) Query(ctx context.Context, s f.Scope, q f.Query) (result f.QueryResult, err error) {
	if s.OwnerID == "" || q.Dataset != "platform_records" || len(q.Fields) < 1 || len(q.Fields) > 32 || q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 10000 || len(q.Filters) > 20 {
		return result, f.Fail("PERMISSION_DENIED", "query is outside authorized dataset constraints")
	}
	columns := []string{}
	seen := map[string]bool{}
	for _, name := range q.Fields {
		expr, ok := dataFields[name]
		if !ok || seen[name] {
			return result, f.Fail("PERMISSION_DENIED", "field is not authorized")
		}
		seen[name] = true
		columns = append(columns, expr+" AS "+pgx.Identifier{name}.Sanitize())
	}
	args := []any{s.OwnerID}
	where := []string{"r.user_id=$1", "c.enabled", "c.state='active'", "NOT u.disabled"}
	for _, filter := range q.Filters {
		expr, ok := dataFields[filter.Field]
		if !ok {
			return result, f.Fail("PERMISSION_DENIED", "filter field is not authorized")
		}
		operator := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[filter.Operator]
		value, ok := filter.Value.(string)
		if !ok || len(value) > 4096 {
			return result, f.Fail("INVALID_ARGUMENT", "dataset filters require bounded string values")
		}
		args = append(args, value)
		parameter := fmt.Sprintf("$%d", len(args))
		if filter.Operator == "contains" {
			where = append(where, "strpos("+expr+","+parameter+")>0")
		} else if operator != "" {
			where = append(where, expr+operator+parameter)
		} else {
			return result, f.Fail("INVALID_ARGUMENT", "unsupported filter operator")
		}
	}
	args = append(args, q.Limit, q.Offset)
	sql := `SELECT ` + strings.Join(columns, ",") + ` FROM platform_records r JOIN platform_connections c ON c.user_id=r.user_id AND c.id=r.connection_id JOIN users u ON u.id=r.user_id WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY r.id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result.Rows = []map[string]any{}
	err = agentError(r.db.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET TRANSACTION READ ONLY`); e != nil {
			return agentError(e)
		}
		if _, e := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); e != nil {
			return agentError(e)
		}
		rows, e := tx.Query(ctx, sql, args...)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			values, e := rows.Values()
			if e != nil {
				return agentError(e)
			}
			row := map[string]any{}
			for i, name := range q.Fields {
				row[name] = values[i]
			}
			result.Rows = append(result.Rows, row)
		}
		return agentError(rows.Err())
	}))
	return
}
