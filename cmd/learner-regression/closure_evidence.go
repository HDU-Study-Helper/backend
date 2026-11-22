package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// closureEvidenceFetch must read one authorized platform record by both its
// central ID and connection ID. The caller supplies the trusted owner scope.
// A missing or inaccessible record must return an error, never an empty match.
type closureEvidenceFetch func(context.Context, string, string) (map[string]any, error)

// closureValidateEvidence checks explicit platform-record reference mappings in
// JSON results or memory snapshots. It does not infer datasets from IDs, rewrite
// references, or judge the factual content of the cited record.
func closureValidateEvidence(ctx context.Context, raw json.RawMessage, fetch closureEvidenceFetch) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("evidence validation requires valid JSON: %w", err)
	}
	type key struct{ id, connection string }
	cache := map[key]map[string]any{}
	var walk func(any, string) error
	walk = func(value any, path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch current := value.(type) {
		case map[string]any:
			dataset, _ := current["dataset"].(string)
			id, _ := current["id"].(string)
			external, _ := current["external_id"].(string)
			connection, _ := current["connection_id"].(string)
			if (dataset == "platform_records" || dataset == "" && connection != "") && id != "" && external != "" {
				if connection == "" {
					return fmt.Errorf("%s: platform record reference with external_id lacks connection_id", path)
				}
				lookup := key{id, connection}
				row, found := cache[lookup]
				if !found {
					if fetch == nil {
						return fmt.Errorf("%s: platform record evidence reader is unavailable", path)
					}
					var err error
					row, err = fetch(ctx, id, connection)
					if err != nil {
						if dataset == "" {
							return nil
						} // Unidentified legacy scopes remain compatible.
						return fmt.Errorf("%s: cannot verify platform record %q on connection %q: %w", path, id, connection, err)
					}
					cache[lookup] = row
				}
				actualID, _ := row["id"].(string)
				actualConnection, _ := row["connection_id"].(string)
				actualExternal, _ := row["external_id"].(string)
				if actualID != id || actualConnection != connection || actualExternal != external {
					return fmt.Errorf("%s: platform record %q reference mapping differs from its authorized record", path, id)
				}
			}
			for name, nested := range current {
				if err := walk(nested, path+"."+name); err != nil {
					return err
				}
			}
		case []any:
			for index, nested := range current {
				if err := walk(nested, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, "$")
}
