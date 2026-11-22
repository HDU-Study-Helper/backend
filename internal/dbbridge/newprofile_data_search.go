package dbbridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	f "academic/agent/framework"
	"github.com/jackc/pgx/v5"
)

var _ f.KeywordSearchService = (*agentData)(nil)

var keywordDatasetOrder = []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"}

type keywordCursor struct {
	Version int    `json:"v"`
	Binding string `json:"binding"`
	Dataset int    `json:"dataset"`
	Key     string `json:"key"`
}

func keywordSearchPlan(owner string, request f.KeywordSearchRequest) ([]string, []string, keywordCursor, error) {
	invalid := func() ([]string, []string, keywordCursor, error) {
		return nil, nil, keywordCursor{}, f.Fail("INVALID_ARGUMENT", "invalid keyword search constraints or cursor")
	}
	if owner == "" || request.Limit < 1 || request.Limit > 100 || len(request.Keywords) < 1 || len(request.Keywords) > 20 || len(request.Datasets) > len(keywordDatasetOrder) || len(request.Cursor) > 8192 {
		return invalid()
	}
	keywords := []string{}
	seen := map[string]bool{}
	for _, key := range request.Keywords {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > 256 {
			return invalid()
		}
		if !seen[key] {
			keywords = append(keywords, key)
			seen[key] = true
		}
	}
	sort.Strings(keywords)
	chosen := map[string]bool{}
	for _, name := range request.Datasets {
		known := false
		for _, allowed := range keywordDatasetOrder {
			if allowed == name {
				known = true
				break
			}
		}
		if !known || chosen[name] {
			return invalid()
		}
		chosen[name] = true
	}
	datasets := []string{}
	for _, name := range keywordDatasetOrder {
		if len(chosen) == 0 || chosen[name] {
			datasets = append(datasets, name)
		}
	}
	bindingBytes, _ := json.Marshal([]any{owner, keywords, datasets})
	sum := sha256.Sum256(bindingBytes)
	cursor := keywordCursor{Version: 1, Binding: hex.EncodeToString(sum[:])}
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil {
			return invalid()
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		var supplied keywordCursor
		if decoder.Decode(&supplied) != nil || supplied.Version != 1 || supplied.Binding != cursor.Binding || supplied.Dataset < 0 || supplied.Dataset >= len(datasets) || supplied.Key == "" || len(supplied.Key) > 4096 {
			return invalid()
		}
		cursor = supplied
	}
	return keywords, datasets, cursor, nil
}

// Filter stored JSON leaves directly for discovery. Reconstructing the entire
// safe metadata response for every scanned row is needlessly expensive.
func keywordMetadataValues(document string) string {
	parts := []string{}
	keys := []string{}
	for _, key := range safeSourceMetadataKeys {
		keys = append(keys, "'"+key+"'")
	}
	parts = append(parts, "SELECT kv.value #>> '{}' AS value FROM jsonb_each(CASE WHEN jsonb_typeof("+document+")='object' THEN "+document+" ELSE '{}'::jsonb END) kv WHERE kv.key IN ("+strings.Join(keys, ",")+") AND jsonb_typeof(kv.value) IN ('string','number','boolean')")
	for _, outer := range []string{"native_progress_fields", "current_user_participation_fields"} {
		keys = []string{}
		for _, key := range safeProgressMetadataKeys {
			keys = append(keys, "'"+key+"'")
		}
		expr := "(" + document + ")->'" + outer + "'"
		parts = append(parts, "SELECT kv.value #>> '{}' FROM jsonb_each(CASE WHEN jsonb_typeof("+expr+")='object' THEN "+expr+" ELSE '{}'::jsonb END) kv WHERE kv.key IN ("+strings.Join(keys, ",")+") AND jsonb_typeof(kv.value) IN ('string','number','boolean')")
	}
	for _, outer := range []string{"node", "test"} {
		keys = []string{}
		for _, key := range safeAssessmentTimeKeys {
			keys = append(keys, "'"+key+"'")
		}
		expr := "(" + document + ")->'assessment_time_fields'->'" + outer + "'"
		parts = append(parts, "SELECT kv.value #>> '{}' FROM jsonb_each(CASE WHEN jsonb_typeof("+expr+")='object' THEN "+expr+" ELSE '{}'::jsonb END) kv WHERE kv.key IN ("+strings.Join(keys, ",")+") AND jsonb_typeof(kv.value) IN ('string','number','boolean')")
	}
	state := "(" + document + ")->'current_user_state'"
	parts = append(parts, "SELECT kv.value #>> '{}' FROM jsonb_each(CASE WHEN jsonb_typeof("+state+")='object' THEN "+state+" ELSE '{}'::jsonb END) kv WHERE kv.key IN ('hasFollowed','hasVoteUp','hasVoteDown') AND jsonb_typeof(kv.value)='boolean'")
	path := "(" + document + ")->'path_ids'"
	parts = append(parts, "SELECT kv.value #>> '{}' FROM jsonb_array_elements(CASE WHEN jsonb_typeof("+path+")='array' THEN "+path+" ELSE '[]'::jsonb END) kv(value) WHERE jsonb_typeof(kv.value) IN ('string','number','boolean')")
	return "(SELECT string_agg(safe.value,' ') FROM (" + strings.Join(parts, " UNION ALL ") + ") safe)"
}

func keywordPredicate(set dataDataset, keywords []string, args *[]any) string {
	// PostgreSQL evaluates every ARRAY element before unnest. Keep JSON scans
	// behind CASE so a match in ordinary fields never reconstructs metadata.
	// CASE (rather than OR) guarantees the expensive branch is short-circuited.
	cheap, expensive := []string{}, []string{}
	for _, field := range set.search {
		expr := set.fields[field]
		if field == "metadata" {
			document := "a.metadata"
			if strings.HasPrefix(set.from, "platform_records r") {
				document = "r.content->'fields'"
			}
			expr = keywordMetadataValues(document)
		} else if field == "details" && strings.HasPrefix(set.from, "academic_offerings a") {
			expr = "(SELECT string_agg(scalar.value #>> '{}',' ') FROM jsonb_path_query((" + expr + ")::jsonb,'$.** ? (@.type() == \"string\" || @.type() == \"number\" || @.type() == \"boolean\")') scalar(value))"
		}
		value := "COALESCE((" + expr + ")::text,'')"
		if field == "metadata" || field == "details" && strings.HasPrefix(set.from, "academic_offerings a") {
			expensive = append(expensive, value)
		} else {
			cheap = append(cheap, value)
		}
	}
	parameters := []string{}
	for _, word := range keywords {
		*args = append(*args, word)
		parameters = append(parameters, fmt.Sprintf("$%d::text", len(*args)))
	}
	match := func(expressions []string) string {
		return "EXISTS (SELECT 1 FROM unnest(ARRAY[" + strings.Join(expressions, ",") + "]::text[]) search_value(value) CROSS JOIN unnest(ARRAY[" + strings.Join(parameters, ",") + "]::text[]) search_word(word) WHERE strpos(lower(search_value.value),search_word.word)>0)"
	}
	if len(expensive) == 0 {
		return match(cheap)
	}
	return "(CASE WHEN " + match(cheap) + " THEN true ELSE " + match(expensive) + " END)"
}

func keywordSummaryFields(dataset string) []string {
	switch dataset {
	case "platform_records":
		return []string{"id", "connection_id", "source", "kind", "external_id", "parent_id", "title", "text", "course_name", "deadline"}
	case "academic_offerings":
		return []string{"id", "connection_id", "source", "course_ref", "course_name", "native_course_id", "native_class_id", "native_term_id", "teacher", "description", "details"}
	case "academic_items":
		return []string{"id", "connection_id", "source", "offering_id", "course_ref", "course_name", "kind", "title", "text", "deadline"}
	case "schedules":
		return []string{"id", "title", "details", "source", "academic_year", "term", "teaching_week", "start_time", "end_time", "event_status"}
	case "dws_messages":
		return []string{"id", "connection_id", "source", "group_id", "group_name", "external_id", "text", "sender", "create_time_raw", "update_time_raw", "reply_id", "quoted_text", "forwarded_text", "attachment_names"}
	case "dws_attachments":
		return []string{"id", "message_id", "connection_id", "source", "group_id", "group_name", "name", "media_type", "sha256", "size_bytes"}
	}
	return nil
}

func keywordSummaryRow(row map[string]any) map[string]any {
	truncated := []string{}
	for name, value := range row {
		text, ok := value.(string)
		if !ok || len(text) <= 1024 || name == "id" || name == "message_id" {
			continue
		}
		end := 1024
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		row[name] = text[:end]
		truncated = append(truncated, name)
	}
	if len(truncated) > 0 {
		sort.Strings(truncated)
		row["_truncated_fields"] = truncated
	}
	return row
}

// SearchKeywords discovers an OR union across all authorized business sources.
// Hits are bounded discovery summaries; _truncated_fields identifies values
// that require data_query for the complete business field.
// Keyset ordering is stable for unchanged rows, with no offset ceiling. Each
// call uses one consistent database snapshot; pages observe current source
// data, so a cursor is not a promise of an immutable multi-call snapshot.
func (r *agentData) SearchKeywords(ctx context.Context, scope f.Scope, request f.KeywordSearchRequest) (f.KeywordSearchResult, error) {
	result := f.KeywordSearchResult{Hits: []f.KeywordSearchHit{}, Coverage: []f.KeywordSearchCoverage{}}
	keywords, datasets, cursor, err := keywordSearchPlan(scope.OwnerID, request)
	if err != nil {
		return result, err
	}
	sets := agentDatasets()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='30s'"); err != nil {
			return err
		}
		// These bounded scans use many safe JSON expressions. PostgreSQL JIT
		// compilation can dominate small owner-scoped datasets by seconds.
		if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
			return err
		}
		// Counts describe only currently authorized keyword matches, never global
		// table size or inaccessible connections/groups.
		for _, name := range datasets {
			set := sets[name]
			args := []any{scope.OwnerID}
			where := append([]string{}, set.where...)
			where = append(where, firstDataBounds(ctx, scope.OwnerID, name, &args)...)
			where = append(where, keywordPredicate(set, keywords, &args))
			var count int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+set.from+" WHERE "+strings.Join(where, " AND "), args...).Scan(&count); err != nil {
				return err
			}
			result.Coverage = append(result.Coverage, f.KeywordSearchCoverage{Dataset: name, Matched: count})
		}
		last := cursor
		moreDataset := -1
		responseBytes := 0
		for index := cursor.Dataset; index < len(datasets); index++ {
			name := datasets[index]
			set := sets[name]
			args := []any{scope.OwnerID}
			where := append([]string{}, set.where...)
			where = append(where, firstDataBounds(ctx, scope.OwnerID, name, &args)...)
			where = append(where, keywordPredicate(set, keywords, &args))
			if index == cursor.Dataset && cursor.Key != "" {
				args = append(args, cursor.Key)
				where = append(where, set.key+">"+fmt.Sprintf("$%d", len(args)))
			}
			fields := keywordSummaryFields(name)
			cols, err := queryProjection(set, fields)
			if err != nil {
				return err
			}
			args = append(args, request.Limit-len(result.Hits)+1)
			sql := "SELECT " + strings.Join(cols, ",") + "," + set.key + " FROM " + set.from + " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + set.key + fmt.Sprintf(" LIMIT $%d", len(args))
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				values, err := rows.Values()
				if err != nil {
					rows.Close()
					return err
				}
				if len(result.Hits) == request.Limit {
					moreDataset = index
					break
				}
				key, ok := values[len(fields)].(string)
				if !ok {
					rows.Close()
					return f.Fail("INTERNAL", "invalid dataset search key")
				}
				hit := f.KeywordSearchHit{Dataset: name, Row: keywordSummaryRow(scanDataRow(values[:len(fields)], fields, set.times))}
				encoded, err := json.Marshal(hit)
				if err != nil {
					rows.Close()
					return err
				}
				// Keep the complete response below the normal 64 KiB tool
				// budget, including coverage, cursor and JSON envelopes.
				if len(encoded) > 48*1024 {
					rows.Close()
					return f.Fail("INVALID_ARGUMENT", "one search hit exceeds response budget; narrow the query or read selected fields by exact ID")
				}
				if responseBytes+len(encoded) > 48*1024 && len(result.Hits) > 0 {
					moreDataset = index
					break
				}
				result.Hits = append(result.Hits, hit)
				responseBytes += len(encoded)
				last = keywordCursor{Version: cursor.Version, Binding: cursor.Binding, Dataset: index, Key: key}
			}
			rowsErr := rows.Err()
			rows.Close()
			if rowsErr != nil {
				return rowsErr
			}
			if moreDataset >= 0 {
				break
			}
		}
		if moreDataset >= 0 {
			result.HasMore = true
			raw, _ := json.Marshal(last)
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		for index := range result.Coverage {
			result.Coverage[index].Complete = moreDataset < 0 || index < moreDataset || result.Coverage[index].Matched == 0
		}
		return nil
	})
	if err != nil {
		return f.KeywordSearchResult{Hits: []f.KeywordSearchHit{}, Coverage: []f.KeywordSearchCoverage{}}, agentError(err)
	}
	return result, nil
}
