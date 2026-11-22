package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	f "academic/agent/framework"
	"academic/backend/internal/continuation"
	stage "academic/backend/internal/learnerstage"
	"github.com/jackc/pgx/v5"
)

func learnerSnapshotTx(ctx context.Context, tx pgx.Tx, s f.Scope, id string) (out stage.Snapshot, err error) {
	out.Memories = []f.Memory{}
	out.Relations = []f.MemoryRelation{}
	if err = activeAgent(ctx, tx, s); err != nil {
		return
	}
	out.Workflow, err = scanWorkflow(tx.QueryRow(ctx, `SELECT `+workflowCols+` FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, s.OwnerID, id))
	if err != nil {
		return
	}
	rows, e := tx.Query(ctx, `SELECT `+memoryCols+` FROM memories m WHERE m.owner_user_id=$1 AND m.owner_workflow_id=$2 ORDER BY m.memory_id`, s.OwnerID, id)
	if e != nil {
		return out, agentError(e)
	}
	for rows.Next() {
		m, e := scanMemory(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Memories = append(out.Memories, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, agentError(e)
	}
	// Only internal target relations are editable. Cross-scope relations are not
	// copied into a target edit, and never confer authority to their other endpoint.
	rows, e = tx.Query(ctx, `SELECT `+relationCols+` FROM memory_relations r JOIN memories a ON(a.owner_user_id,a.memory_id)=(r.owner_user_id,r.source_memory_id) JOIN memories b ON(b.owner_user_id,b.memory_id)=(r.owner_user_id,r.target_memory_id) WHERE r.owner_user_id=$1 AND a.owner_workflow_id=$2 AND b.owner_workflow_id=$2 ORDER BY r.relation_id`, s.OwnerID, id)
	if e != nil {
		return out, agentError(e)
	}
	for rows.Next() {
		r, e := scanRelation(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Relations = append(out.Relations, r)
	}
	e = rows.Err()
	rows.Close()
	return out, agentError(e)
}
func (r *agents) LearnerSnapshot(ctx context.Context, s f.Scope, id string) (out stage.Snapshot, err error) {
	err = r.tx(ctx, func(tx pgx.Tx) error { var e error; out, e = learnerSnapshotTx(ctx, tx, s, id); return e })
	return
}
func (r *agents) LearnerStageReceipt(ctx context.Context, owner, id string) (out stage.Receipt, err error) {
	var raw []byte
	err = r.tx(ctx, func(tx pgx.Tx) error {
		if e := activeOwner(ctx, tx, owner); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT receipt FROM learner_stage_commits WHERE owner_user_id=$1 AND session_id=$2`, owner, id).Scan(&raw); e != nil {
			return agentError(e)
		}
		return json.Unmarshal(raw, &out)
	})
	return
}

func (r *agents) CommitLearnerStage(ctx context.Context, c stage.Commit) (out stage.Receipt, err error) {
	if c.SessionID == "" || c.Scope.OwnerID == "" || c.Scope.AgentID == "" || c.Revision < 1 {
		return out, f.Fail("INVALID_ARGUMENT", "invalid staging commit")
	}
	raw, e := json.Marshal(c)
	if e != nil || len(raw) > 8<<20 {
		return out, f.Fail("INVALID_ARGUMENT", "staging commit exceeds budget")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	err = r.tx(ctx, func(tx pgx.Tx) error {
		// Same lock order as existing workflow+memory writers. This compares the
		// whole selected PRI baseline, including additions/deletions and relations.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,730022405))`, c.Scope.OwnerID); e != nil {
			return agentError(e)
		}
		if e := memoryLock(ctx, tx, c.Scope); e != nil {
			return e
		}
		if e := activeAgent(ctx, tx, c.Scope); e != nil {
			return e
		}
		var agent f.AgentInstance
		agent, e = scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM agent_instances a JOIN users u ON u.id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND NOT u.disabled FOR SHARE OF a,u`, c.Scope.OwnerID, c.Scope.AgentID))
		if e != nil {
			return e
		}
		if agent.Status != "enabled" || agent.Type != "learner" || !hasString(agent.Permissions, "workflow.write") || !hasString(agent.Permissions, "memory.write") {
			return f.Fail("PERMISSION_DENIED", "Learner staging permissions revoked")
		}
		var storedHash string
		var receipt []byte
		e = tx.QueryRow(ctx, `SELECT request_hash,receipt FROM learner_stage_commits WHERE owner_user_id=$1 AND session_id=$2`, c.Scope.OwnerID, c.SessionID).Scan(&storedHash, &receipt)
		if e == nil {
			if storedHash != hash {
				return f.Fail("CONFLICT", "session was committed with a different revision or payload")
			}
			return json.Unmarshal(receipt, &out)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return agentError(e)
		}
		target := c.Desired.Workflow.ID
		if c.Base != nil {
			current, e := learnerSnapshotTx(ctx, tx, c.Scope, c.Base.Workflow.ID)
			if e != nil {
				return e
			}
			if stage.Hash(current) != c.BaselineHash || stage.Hash(*c.Base) != c.BaselineHash {
				return f.Fail("CONFLICT", "Workflow or private memories changed after staging selection")
			}
			if target != c.Base.Workflow.ID {
				return f.Fail("PERMISSION_DENIED", "staging target cannot change")
			}
		} else if c.Operation != "CREATE" {
			return f.Fail("INVALID_ARGUMENT", "existing baseline required")
		}
		out = stage.Receipt{SessionID: c.SessionID, Revision: c.Revision, Operation: c.Operation, WorkflowID: target, MemoryIDs: []string{}, IDMap: map[string]string{}, Verified: true, Status: "COMMITTED"}
		switch c.Operation {
		case "NONE":
			if c.Base == nil || stage.Hash(c.Desired) != c.BaselineHash {
				return f.Fail("INVALID_ARGUMENT", "NO_CHANGE requires an empty diff")
			}
			out.WorkflowVersion = c.Base.Workflow.Version
			out.Status = "NO_CHANGE"
		case "DELETE":
			if !hasString(agent.Permissions, "workflow.delete") || !hasString(agent.EnabledTools, "workflow_delete") || c.Base == nil {
				return f.Fail("PERMISSION_DENIED", "Workflow delete permission required")
			}
			if _, e := tx.Exec(ctx, `DELETE FROM agent_workflows WHERE owner_user_id=$1 AND workflow_id=$2`, c.Scope.OwnerID, target); e != nil {
				return agentError(e)
			}
			out.WorkflowVersion = c.Base.Workflow.Version
			out.Status = "DELETED"
		case "CREATE", "UPDATE", "ARCHIVE":
			w := c.Desired.Workflow
			if c.Operation == "CREATE" && c.Base != nil || c.Operation != "CREATE" && c.Base == nil {
				return f.Fail("INVALID_ARGUMENT", "operation does not match baseline")
			}
			if c.Operation == "ARCHIVE" {
				w.Status = "archived"
			}
			originalID := w.ID
			if c.Operation == "CREATE" {
				w.ID = ""
				w.Version = 0
			} else {
				w.Version = c.Base.Workflow.Version
			}
			saved := w
			definitionChanged := c.Base == nil || w.Name != c.Base.Workflow.Name || w.Description != c.Base.Workflow.Description || w.Status != c.Base.Workflow.Status || w.ParentID != c.Base.Workflow.ParentID || !sameStageValue(w.Definition, c.Base.Workflow.Definition)
			if definitionChanged {
				requiredTool := "workflow_update"
				if c.Operation == "CREATE" {
					requiredTool = "workflow_create"
				}
				if !hasString(agent.EnabledTools, requiredTool) {
					return f.Fail("PERMISSION_DENIED", "required Workflow tool was revoked")
				}
				var e error
				saved, e = saveWorkflowTx(ctx, tx, c.Scope, w, c.Operation == "CREATE")
				if e != nil {
					return e
				}
			} else {
				saved = c.Base.Workflow
			}
			out.WorkflowID = saved.ID
			out.WorkflowVersion = saved.Version
			out.IDMap[originalID] = saved.ID
			ms := append([]f.Memory(nil), c.Desired.Memories...)
			sort.SliceStable(ms, func(i, j int) bool {
				if ms[i].Level != ms[j].Level {
					return ms[i].Level < ms[j].Level
				}
				return ms[i].ID < ms[j].ID
			})
			existing := map[string]f.Memory{}
			if c.Base != nil {
				for _, m := range c.Base.Memories {
					existing[m.ID] = m
				}
			}
			desired := map[string]bool{}
			for _, m := range ms {
				if m.ID == "" || desired[m.ID] {
					return f.Fail("INVALID_ARGUMENT", "duplicate staged memory")
				}
				desired[m.ID] = true
				if _, ok := existing[m.ID]; ok {
					out.IDMap[m.ID] = m.ID
				} else {
					out.IDMap[m.ID] = f.NewID()
				}
			}
			// Remove only target-internal relations; unrelated cross-scope edges survive
			// unless their memory endpoint is explicitly deleted by this commit.
			desiredRelations := map[string]f.MemoryRelation{}
			for _, v := range c.Desired.Relations {
				desiredRelations[v.ID] = v
			}
			existingRelations := map[string]f.MemoryRelation{}
			if c.Base != nil {
				for _, v := range c.Base.Relations {
					existingRelations[v.ID] = v
					if next, ok := desiredRelations[v.ID]; ok && sameStageValue(v, next) {
						continue
					}
					if _, e := tx.Exec(ctx, `DELETE FROM memory_relations WHERE owner_user_id=$1 AND relation_id=$2`, c.Scope.OwnerID, v.ID); e != nil {
						return agentError(e)
					}
				}
				for _, m := range c.Base.Memories {
					if !desired[m.ID] {
						if !hasString(agent.EnabledTools, "memory_delete") {
							return f.Fail("PERMISSION_DENIED", "memory_delete was revoked")
						}
						if _, e := tx.Exec(ctx, `DELETE FROM memories WHERE owner_user_id=$1 AND owner_workflow_id=$2 AND memory_id=$3`, c.Scope.OwnerID, target, m.ID); e != nil {
							return agentError(e)
						}
					}
				}
			}
			for _, m := range ms {
				oldID := m.ID
				if m.OwnerID != "" && m.OwnerID != c.Scope.OwnerID || m.AgentID != "" || m.WorkflowID != target || m.Visibility != "PRI" {
					return f.Fail("PERMISSION_DENIED", "staged memory ownership mismatch")
				}
				m.ID = out.IDMap[oldID]
				m.OwnerID = c.Scope.OwnerID
				m.WorkflowID = saved.ID
				if m.ParentID != "" {
					if mapped, ok := out.IDMap[m.ParentID]; ok {
						m.ParentID = mapped
					} else {
						var visible bool
						if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memories WHERE owner_user_id=$1 AND memory_id=$2 AND visibility='PUB')`, c.Scope.OwnerID, m.ParentID).Scan(&visible); e != nil {
							return agentError(e)
						}
						if !visible {
							return f.Fail("PERMISSION_DENIED", "memory parent is outside target and user PUB")
						}
					}
				}
				if e := normalizeMemoryFields(&m); e != nil {
					return e
				}
				if e := validateMemorySourceRefsTx(ctx, tx, c.Scope.OwnerID, m.SourceRefs); e != nil {
					return e
				}
				if old, exists := existing[oldID]; exists && sameStageMemory(old, m) {
					out.MemoryIDs = append(out.MemoryIDs, m.ID)
					continue
				}
				requiredTool := "memory_create"
				if _, exists := existing[oldID]; exists {
					requiredTool = "memory_update"
				}
				if !hasString(agent.EnabledTools, requiredTool) {
					return f.Fail("PERMISSION_DENIED", "required memory tool was revoked")
				}
				args := memoryArgs(m)
				sql := insertMemorySQL
				if _, ok := existing[oldID]; ok {
					sql = `UPDATE memories m SET visibility=$3,owner_agent_id=NULLIF($4,''),owner_workflow_id=NULLIF($5,''),memory_type=$6,is_personal=$7,level=$8,parent_memory_id=NULLIF($9,''),title=$10,content=$11,structured_content=$12,source_refs=$13,valid_from=$14,valid_until=$15,status=$16,category=$17,priority=$18,updated_at=clock_timestamp() WHERE m.memory_id=$1 AND m.owner_user_id=$2 RETURNING ` + memoryCols
				}
				if _, e := scanMemory(tx.QueryRow(ctx, sql, args...)); e != nil {
					return e
				}
				out.MemoryIDs = append(out.MemoryIDs, m.ID)
			}
			for _, v := range c.Desired.Relations {
				if old, exists := existingRelations[v.ID]; exists && sameStageValue(old, v) {
					continue
				}
				if !hasString(agent.EnabledTools, "memory_create_relation") {
					return f.Fail("PERMISSION_DENIED", "memory_create_relation was revoked")
				}
				source, ok1 := out.IDMap[v.SourceID]
				dest, ok2 := out.IDMap[v.TargetID]
				if !ok1 || !ok2 || source == dest || !validRelation(v.Type) {
					return f.Fail("INVALID_ARGUMENT", "invalid staged relation")
				}
				id := v.ID
				if id == "" {
					id = f.NewID()
				}
				if _, e := tx.Exec(ctx, `INSERT INTO memory_relations(relation_id,owner_user_id,source_memory_id,target_memory_id,relation_type,description,created_by_agent_id)VALUES($1,$2,$3,$4,$5,$6,$7)`, id, c.Scope.OwnerID, source, dest, v.Type, v.Description, c.Scope.AgentID); e != nil {
					return agentError(e)
				}
			}
		default:
			return f.Fail("INVALID_ARGUMENT", "unsupported staging operation")
		}
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		if bind := continuation.Binding(ctx); bind != nil {
			a, q := bind(out.WorkflowID, raw)
			if q != nil {
				if err := saveContinuationTx(ctx, tx, a, q); err != nil {
					return err
				}
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO learner_stage_commits(owner_user_id,session_id,request_hash,receipt)VALUES($1,$2,$3,$4)`, c.Scope.OwnerID, c.SessionID, hash, raw)
		return agentError(e)
	})
	return
}

func sameStageValue(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func sameStageMemory(a, b f.Memory) bool { a.UpdatedAt = b.UpdatedAt; return sameStageValue(a, b) }

var _ stage.Store = (*agents)(nil)
