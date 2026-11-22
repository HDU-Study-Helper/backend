package dbbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

// normalizeWorkflowProfileSeed deliberately accepts no selectable ownership,
// existing IDs, parent links or inactive/dated records. It validates content
// independently of ordinary memory ACLs; it never manufactures a caller Scope.
func normalizeWorkflowProfileSeed(m f.Memory) (f.Memory, error) {
	if m.ID != "" || m.OwnerID != "" || m.AgentID != "" || m.WorkflowID != "" || m.ParentID != "" || (m.Visibility != "" && m.Visibility != "PRI") {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "initial private memory ownership and IDs are server assigned")
	}
	if (m.Status != "" && m.Status != "active") || m.ValidFrom != nil || m.ValidUntil != nil {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "initial private memories must be active and currently valid")
	}
	m.Visibility = "PRI"
	if err := normalizeMemoryFields(&m); err != nil {
		return f.Memory{}, err
	}
	var refs []json.RawMessage
	if json.Unmarshal(m.SourceRefs, &refs) != nil || len(refs) == 0 || len(refs) > 100 {
		return f.Memory{}, f.Fail("INVALID_ARGUMENT", "initial private memory source_refs require 1 to 100 references")
	}
	for _, raw := range refs {
		var text string
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" && len(text) <= 8192 {
			continue
		}
		var ref map[string]json.RawMessage
		if len(raw) > 8192 || json.Unmarshal(raw, &ref) != nil || len(ref) == 0 || len(ref) > 32 {
			return f.Memory{}, f.Fail("INVALID_ARGUMENT", "source references must be bounded nonempty strings or objects")
		}
	}
	return m, nil
}

func workflowProfileAgent(ctx context.Context, tx pgx.Tx, s f.Scope) error {
	// Lock the same agent/config and owner rows for the whole transaction, so
	// permission revocation cannot race a bootstrap after this check.
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM agent_instances a JOIN users u ON u.id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.agent_id=$2 AND NOT u.disabled FOR SHARE OF a,u`, s.OwnerID, s.AgentID))
	if err != nil {
		return err
	}
	if a.Status != "enabled" || !hasString(a.EnabledTools, "workflow_create") || !hasString(a.Permissions, "workflow.write") || !hasString(a.Permissions, "memory.write") {
		return f.Fail("PERMISSION_DENIED", "atomic profile creation requires an enabled agent with workflow_create, workflow.write and memory.write")
	}
	return nil
}

func hasString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func (r *agents) CreateWorkflowWithMemories(ctx context.Context, s f.Scope, w f.Workflow, seeds []f.Memory) (out f.WorkflowProfileCreation, err error) {
	if len(seeds) == 0 || len(seeds) > f.MaxWorkflowPrivateMemories || w.ID != "" || w.OwnerID != "" || w.CreatedBy != "" || w.Version != 0 || w.Status == "archived" {
		return out, f.Fail("INVALID_ARGUMENT", "invalid workflow profile bootstrap")
	}
	if e := validateInitialCourseProfile(w, seeds); e != nil {
		return out, e
	}
	raw, e := json.Marshal(seeds)
	if e != nil || len(raw) > f.MaxWorkflowPrivateMemoryBytes {
		return out, f.Fail("INVALID_ARGUMENT", "initial private memories exceed byte budget")
	}
	prepared := make([]f.Memory, len(seeds))
	for i, seed := range seeds {
		prepared[i], e = normalizeWorkflowProfileSeed(seed)
		if e != nil {
			return out, e
		}
	}
	err = r.tx(ctx, func(tx pgx.Tx) error {
		// Use the same owner lock ordering as ordinary workflow writes.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,730022405))`, s.OwnerID); e != nil {
			return agentError(e)
		}
		if e := workflowProfileAgent(ctx, tx, s); e != nil {
			return e
		}
		created, e := saveWorkflowTx(ctx, tx, s, w, true)
		if e != nil {
			return e
		}
		if e := memoryLock(ctx, tx, s); e != nil {
			return e
		}
		persisted := make([]f.Memory, len(prepared))
		ids := make([]string, len(prepared))
		for i, seed := range prepared {
			if e := validateMemorySourceRefsTx(ctx, tx, s.OwnerID, seed.SourceRefs); e != nil {
				return e
			}
			seed.ID, seed.OwnerID, seed.WorkflowID = f.NewID(), s.OwnerID, created.ID
			persisted[i], e = scanMemory(tx.QueryRow(ctx, insertMemorySQL, memoryArgs(seed)...))
			if e != nil {
				return e
			}
			ids[i] = persisted[i].ID
		}
		// Canonicalize JSONB object ordering before hashing the actual returned rows.
		encoded, e := json.Marshal(persisted)
		if e != nil {
			return e
		}
		var canonical any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if e = decoder.Decode(&canonical); e != nil {
			return e
		}
		encoded, e = json.Marshal(canonical)
		if e != nil {
			return e
		}
		digest := sha256.Sum256(encoded)
		out = f.WorkflowProfileCreation{Workflow: created, ProfileInitialization: f.WorkflowProfileReceipt{
			MemoryIDs: ids, Count: len(ids), Visibility: "PRI", OwnerWorkflowID: created.ID, Verified: true, ContentSHA256: hex.EncodeToString(digest[:]),
		}}
		return nil
	})
	if err != nil {
		// In particular, never expose an inserted ID or verified receipt after a
		// failed commit. Callers reconcile uncertain commits before retrying.
		return f.WorkflowProfileCreation{}, err
	}
	return out, nil
}

var _ f.WorkflowProfileRepository = (*agents)(nil)
