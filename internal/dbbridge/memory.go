package dbbridge

import (
	"context"
	"encoding/json"
	"strings"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

const memoryCols = `m.memory_id,m.owner_user_id,m.visibility,COALESCE(m.owner_agent_id,''),COALESCE(m.owner_workflow_id,''),m.memory_type,m.is_personal,m.level,COALESCE(m.parent_memory_id,''),m.title,m.content,m.structured_content,m.source_refs,m.valid_from,m.valid_until,m.status,m.category,m.priority,m.created_at,m.updated_at`

func scanMemory(row pgx.Row) (m f.Memory, err error) {
	err = row.Scan(&m.ID, &m.OwnerID, &m.Visibility, &m.AgentID, &m.WorkflowID, &m.MemoryType, &m.IsPersonal, &m.Level, &m.ParentID, &m.Title, &m.Content, &m.StructuredContent, &m.SourceRefs, &m.ValidFrom, &m.ValidUntil, &m.Status, &m.Category, &m.Priority, &m.CreatedAt, &m.UpdatedAt)
	return m, agentError(err)
}

// All SQL predicates use server-bound scope arguments; no tool can choose tenant.
func memoryAccess(alias string) string {
	return alias + `.owner_user_id=$1 AND (` + alias + `.visibility='PUB' OR ` + alias + `.owner_agent_id=$2 OR ($3<>'' AND ` + alias + `.owner_workflow_id=$3))`
}
func memoryScope(ctx context.Context, tx pgx.Tx, s f.Scope) error {
	if err := activeAgent(ctx, tx, s); err != nil {
		return err
	}
	if s.WorkflowID != "" {
		var id string
		return agentError(tx.QueryRow(ctx, `SELECT workflow_id FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2 AND status<>'archived' FOR SHARE`, s.OwnerID, s.WorkflowID).Scan(&id))
	}
	return nil
}
func memoryLock(ctx context.Context, tx pgx.Tx, s f.Scope) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,730022409))`, s.OwnerID)
	return agentError(err)
}
func normalizeMemory(s f.Scope, m *f.Memory) error {
	if m.OwnerID != "" && m.OwnerID != s.OwnerID {
		return f.Fail("PERMISSION_DENIED", "memory tenant cannot be changed")
	}
	m.OwnerID = s.OwnerID
	if m.Visibility == "" {
		m.Visibility = "PRI"
	}
	if m.Visibility == "PRI" && m.AgentID == "" && m.WorkflowID == "" {
		m.AgentID = s.AgentID
	}
	switch m.Visibility {
	case "PUB":
		if m.AgentID != "" || m.WorkflowID != "" {
			return f.Fail("INVALID_ARGUMENT", "PUB memory has no private owner")
		}
	case "PRI":
		if (m.AgentID != "" && m.WorkflowID != "") || (m.AgentID == "" && m.WorkflowID == "") {
			return f.Fail("INVALID_ARGUMENT", "PRI memory needs exactly one owner")
		}
		if (m.AgentID != "" && m.AgentID != s.AgentID) || (m.WorkflowID != "" && (s.WorkflowID == "" || m.WorkflowID != s.WorkflowID)) {
			return f.Fail("PERMISSION_DENIED", "private owner is outside execution context")
		}
	default:
		return f.Fail("INVALID_ARGUMENT", "invalid visibility")
	}
	return normalizeMemoryFields(m)
}

// Field validation is shared by the narrowly authorized workflow bootstrap;
// ordinary writes must still pass normalizeMemory's execution-scope checks.
func normalizeMemoryFields(m *f.Memory) error {
	if m.MemoryType == "" {
		m.MemoryType = "BASE"
	}
	if m.Level == "" {
		m.Level = "L1"
	}
	if m.Status == "" {
		m.Status = "active"
	}
	if !oneOf(m.MemoryType, "BASE", "TEMP") || !oneOf(m.Level, "L0", "L1", "L2", "L3") || !oneOf(m.Status, "active", "archived", "invalid") || strings.TrimSpace(m.Title) == "" || len(m.Title) > 2048 || strings.TrimSpace(m.Content) == "" || len(m.Content) > 262144 || len(m.Category) > 512 || m.Priority < -100 || m.Priority > 100 || len(m.StructuredContent) > 65536 || len(m.SourceRefs) > 65536 {
		return f.Fail("INVALID_ARGUMENT", "invalid memory fields")
	}
	if m.ValidFrom != nil && m.ValidUntil != nil && !m.ValidUntil.After(*m.ValidFrom) {
		return f.Fail("INVALID_ARGUMENT", "invalid validity interval")
	}
	if len(m.StructuredContent) > 0 {
		var v map[string]any
		if json.Unmarshal(m.StructuredContent, &v) != nil || v == nil {
			return f.Fail("INVALID_ARGUMENT", "structured_content must be an object")
		}
	}
	if len(m.SourceRefs) > 0 {
		var v []any
		if json.Unmarshal(m.SourceRefs, &v) != nil || v == nil {
			return f.Fail("INVALID_ARGUMENT", "source_refs must be an array")
		}
	}
	return nil
}
func oneOf(v string, options ...string) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}
func nullJSON(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

const insertMemorySQL = `INSERT INTO memories AS m(memory_id,owner_user_id,visibility,owner_agent_id,owner_workflow_id,memory_type,is_personal,level,parent_memory_id,title,content,structured_content,source_refs,valid_from,valid_until,status,category,priority) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING ` + memoryCols

func memoryArgs(m f.Memory) []any {
	return []any{m.ID, m.OwnerID, m.Visibility, m.AgentID, m.WorkflowID, m.MemoryType, m.IsPersonal, m.Level, m.ParentID, m.Title, m.Content, nullJSON(m.StructuredContent), nullJSON(m.SourceRefs), m.ValidFrom, m.ValidUntil, m.Status, m.Category, m.Priority}
}

func (r *agents) SaveMemory(ctx context.Context, s f.Scope, m f.Memory, create bool) (out f.Memory, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		if e := memoryLock(ctx, tx, s); e != nil {
			return e
		}
		if !create {
			if _, e := scanMemory(tx.QueryRow(ctx, `SELECT `+memoryCols+` FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4 FOR UPDATE`, s.OwnerID, s.AgentID, s.WorkflowID, m.ID)); e != nil {
				return e
			}
		}
		if e := normalizeMemory(s, &m); e != nil {
			return e
		}
		if e := validateMemorySourceRefsTx(ctx, tx, s.OwnerID, m.SourceRefs); e != nil {
			return e
		}
		// Parent endpoints must be accessible before the SQL trigger verifies organization.
		if m.ParentID != "" {
			var id string
			if e := tx.QueryRow(ctx, `SELECT m.memory_id FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4`, s.OwnerID, s.AgentID, s.WorkflowID, m.ParentID).Scan(&id); e != nil {
				return agentError(e)
			}
		}
		if create {
			m.ID = f.NewID()
		}
		args := memoryArgs(m)
		sql := insertMemorySQL
		if !create {
			sql = `UPDATE memories m SET visibility=$3,owner_agent_id=NULLIF($4,''),owner_workflow_id=NULLIF($5,''),memory_type=$6,is_personal=$7,level=$8,parent_memory_id=NULLIF($9,''),title=$10,content=$11,structured_content=$12,source_refs=$13,valid_from=$14,valid_until=$15,status=$16,category=$17,priority=$18,updated_at=clock_timestamp() WHERE m.memory_id=$1 AND m.owner_user_id=$2 RETURNING ` + memoryCols
		}
		var e error
		out, e = scanMemory(tx.QueryRow(ctx, sql, args...))
		return e
	})
	return
}
func (r *agents) GetMemory(ctx context.Context, s f.Scope, id string) (out f.Memory, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		var e error
		out, e = scanMemory(tx.QueryRow(ctx, `SELECT `+memoryCols+` FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4`, s.OwnerID, s.AgentID, s.WorkflowID, id))
		return e
	})
	return
}
func (r *agents) ListMemory(ctx context.Context, s f.Scope, q string, limit int) ([]f.Memory, error) {
	return r.SearchMemory(ctx, s, f.MemorySearch{Query: q, Limit: limit})
}
func (r *agents) SearchMemory(ctx context.Context, s f.Scope, q f.MemorySearch) (out []f.Memory, err error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Validity == "" {
		q.Validity = "current"
	}
	if q.Status == "" {
		q.Status = "active"
	}
	if q.Limit < 1 || q.Limit > 1000 || len(q.Query) > 2048 || !oneOf(q.Validity, "current", "expired", "future", "all") || !oneOf(q.Status, "active", "archived", "invalid", "all") || (q.Visibility != "" && !oneOf(q.Visibility, "PUB", "PRI")) || (q.MemoryType != "" && !oneOf(q.MemoryType, "BASE", "TEMP")) || (q.Level != "" && !oneOf(q.Level, "L0", "L1", "L2", "L3")) {
		return nil, f.Fail("INVALID_ARGUMENT", "invalid memory search")
	}
	if (q.AgentID != "" && q.AgentID != s.AgentID) || (q.WorkflowID != "" && (q.WorkflowID != s.WorkflowID || s.WorkflowID == "")) {
		return nil, f.Fail("PERMISSION_DENIED", "private scope is outside execution context")
	}
	out = []f.Memory{}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		where := []string{memoryAccess("m")}
		args := []any{s.OwnerID, s.AgentID, s.WorkflowID}
		// Fixed SQL with optional filters; never interpolate caller-controlled identifiers.
		where = append(where, `($4='' OR strpos(lower(m.title||' '||m.content||' '||m.category||' '||COALESCE(m.structured_content::text,'')),lower($4))>0)`, `($5='' OR m.visibility=$5)`, `($6='' OR m.owner_agent_id=$6)`, `($7='' OR m.owner_workflow_id=$7)`, `($8='' OR m.memory_type=$8)`, `($9='' OR m.level=$9)`, `($10='all' OR m.status=$10)`, `($11='' OR m.parent_memory_id=$11)`, `($12::boolean IS NULL OR m.is_personal=$12)`)
		args = append(args, q.Query, q.Visibility, q.AgentID, q.WorkflowID, q.MemoryType, q.Level, q.Status, q.ParentID, q.IsPersonal)
		switch q.Validity {
		case "current":
			where = append(where, `(m.valid_from IS NULL OR m.valid_from<=now()) AND (m.valid_until IS NULL OR m.valid_until>now())`)
		case "expired":
			where = append(where, `m.valid_until<=now()`)
		case "future":
			where = append(where, `m.valid_from>now()`)
		}
		args = append(args, q.Limit)
		rows, e := tx.Query(ctx, `SELECT `+memoryCols+` FROM memories m WHERE `+strings.Join(where, " AND ")+` ORDER BY m.priority DESC,m.created_at,m.memory_id LIMIT $13`, args...)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			m, e := scanMemory(rows)
			if e != nil {
				return e
			}
			out = append(out, m)
		}
		return agentError(rows.Err())
	})
	return
}
func (r *agents) DeleteMemory(ctx context.Context, s f.Scope, id string) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		if e := memoryLock(ctx, tx, s); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `DELETE FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4`, s.OwnerID, s.AgentID, s.WorkflowID, id)
		return affected(tag, e)
	})
}

const relationCols = `r.relation_id,r.owner_user_id,r.source_memory_id,r.target_memory_id,r.relation_type,r.description,r.created_by_agent_id,r.created_at`

func scanRelation(row pgx.Row) (r f.MemoryRelation, err error) {
	err = row.Scan(&r.ID, &r.OwnerID, &r.SourceID, &r.TargetID, &r.Type, &r.Description, &r.CreatedBy, &r.CreatedAt)
	return r, agentError(err)
}
func validRelation(v string) bool {
	return oneOf(v, "RELATED_TO", "ALIAS_OF", "BELONGS_TO", "DERIVED_FROM", "ROUTES_TO", "SUPERSEDES")
}
func (r *agents) CreateMemoryRelation(ctx context.Context, s f.Scope, v f.MemoryRelation) (out f.MemoryRelation, err error) {
	if !validRelation(v.Type) || v.SourceID == "" || v.TargetID == "" || v.SourceID == v.TargetID || len(v.Description) > 16384 {
		return out, f.Fail("INVALID_ARGUMENT", "invalid relation")
	}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		if e := memoryLock(ctx, tx, s); e != nil {
			return e
		}
		for _, id := range []string{v.SourceID, v.TargetID} {
			var found string
			if e := tx.QueryRow(ctx, `SELECT m.memory_id FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4`, s.OwnerID, s.AgentID, s.WorkflowID, id).Scan(&found); e != nil {
				return agentError(e)
			}
		}
		var e error
		out, e = scanRelation(tx.QueryRow(ctx, `INSERT INTO memory_relations AS r(relation_id,owner_user_id,source_memory_id,target_memory_id,relation_type,description,created_by_agent_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+relationCols, f.NewID(), s.OwnerID, v.SourceID, v.TargetID, v.Type, v.Description, s.AgentID))
		return e
	})
	return
}
func (r *agents) GetMemoryRelations(ctx context.Context, s f.Scope, q f.MemoryRelationQuery) (out []f.MemoryRelation, err error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Depth == 0 {
		q.Depth = 1
	}
	if q.Direction == "" {
		q.Direction = "both"
	}
	if q.MemoryID == "" || q.Depth != 1 || q.Limit < 1 || q.Limit > 100 || !oneOf(q.Direction, "both", "incoming", "outgoing") || (q.Type != "" && !validRelation(q.Type)) {
		return nil, f.Fail("INVALID_ARGUMENT", "invalid direct relation query")
	}
	out = []f.MemoryRelation{}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := memoryScope(ctx, tx, s); e != nil {
			return e
		}
		var found string
		if e := tx.QueryRow(ctx, `SELECT m.memory_id FROM memories m WHERE `+memoryAccess("m")+` AND m.memory_id=$4`, s.OwnerID, s.AgentID, s.WorkflowID, q.MemoryID).Scan(&found); e != nil {
			return agentError(e)
		}
		rows, e := tx.Query(ctx, `SELECT `+relationCols+` FROM memory_relations r JOIN memories a ON a.owner_user_id=r.owner_user_id AND a.memory_id=r.source_memory_id JOIN memories b ON b.owner_user_id=r.owner_user_id AND b.memory_id=r.target_memory_id WHERE `+memoryAccess("a")+` AND `+memoryAccess("b")+` AND ($5='' OR r.relation_type=$5) AND (($6 IN ('both','outgoing') AND r.source_memory_id=$4) OR ($6 IN ('both','incoming') AND r.target_memory_id=$4)) ORDER BY r.created_at,r.relation_id LIMIT $7`, s.OwnerID, s.AgentID, s.WorkflowID, q.MemoryID, q.Type, q.Direction, q.Limit)
		if e != nil {
			return agentError(e)
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanRelation(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return agentError(rows.Err())
	})
	return
}
