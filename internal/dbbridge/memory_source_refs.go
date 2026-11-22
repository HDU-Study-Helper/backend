package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

// validateMemorySourceRefsTx checks explicit, fully specified platform record
// mappings, including dataset-free legacy objects that resolve to an authorized
// platform record. This prevents deleting dataset from bypassing a known mapping.
// Unresolved legacy objects, strings, partial references and other datasets keep
// their existing shape and semantics. It never rewrites supplied provenance.
func validateMemorySourceRefsTx(ctx context.Context, tx pgx.Tx, owner string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var refs []mapOrLegacySourceRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		// normalizeMemoryFields owns source_refs format validation. Mixed legacy
		// arrays must not gain a new structural requirement from this check.
		return nil
	}
	type key struct{ id, connection string }
	cache := map[key]string{}
	for index, ref := range refs {
		if ref.fields == nil {
			continue
		}
		var dataset, id, connection, external string
		_ = json.Unmarshal(ref.fields["dataset"], &dataset)
		_ = json.Unmarshal(ref.fields["id"], &id)
		_ = json.Unmarshal(ref.fields["connection_id"], &connection)
		_ = json.Unmarshal(ref.fields["external_id"], &external)
		if (dataset != "platform_records" && dataset != "") || id == "" || connection == "" || external == "" {
			continue
		}
		lookup := key{id, connection}
		actual, exists := cache[lookup]
		if !exists {
			err := tx.QueryRow(ctx, `SELECT p.external_id FROM platform_records p JOIN platform_connections c ON(c.user_id,c.id)=(p.user_id,p.connection_id) WHERE p.user_id=$1 AND p.id::text=$2 AND p.connection_id=$3 AND c.enabled AND c.state='active' FOR SHARE OF p,c`, owner, id, connection).Scan(&actual)
			if errors.Is(err, pgx.ErrNoRows) {
				if dataset == "" {
					// A dataset-free legacy object may refer to another namespace
					// (for example DWS). No authorized platform match means this
					// narrow check cannot classify it and imposes no new shape.
					continue
				}
				return f.Fail("INVALID_ARGUMENT", fmt.Sprintf("source_refs[%d] platform_records id=%q connection_id=%q is not available in the authorized active connection", index, id, connection))
			}
			if err != nil {
				return agentError(err)
			}
			cache[lookup] = actual
		}
		if actual != external {
			return f.Fail("INVALID_ARGUMENT", fmt.Sprintf("source_refs[%d] platform_records id=%q connection_id=%q external_id=%q does not match the actual authorized external_id=%q; correct the reference before saving", index, id, connection, external, actual))
		}
	}
	return nil
}

// Each element may still be a legacy string or another existing JSON value.
// Only explicit objects are inspected; no new reference schema is imposed.
type mapOrLegacySourceRef struct{ fields map[string]json.RawMessage }

func (ref *mapOrLegacySourceRef) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		ref.fields = fields
	}
	return nil
}
