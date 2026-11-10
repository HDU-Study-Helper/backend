package runcontrol

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

type knownEvidenceKey struct{}
type knownAcademicEvidenceKey struct{}

type academicEvidenceReferences struct {
	OwnerID, ConnectionID, RecordID string
	IDs                             map[string]bool
}

func knownAcademicEvidenceReferences(ctx context.Context) academicEvidenceReferences {
	refs, _ := ctx.Value(knownAcademicEvidenceKey{}).(academicEvidenceReferences)
	return refs
}

// knownEvidenceReferences exposes only references verified by host enrichment.
func knownEvidenceReferences(ctx context.Context) map[string]bool {
	refs, _ := ctx.Value(knownEvidenceKey{}).(map[string]bool)
	return refs
}

// enrichItemContext performs exact owner/active-connection queries, never model
// inference. Failed enrichment falls back to the normal authorized tools.
func (s *Service) enrichItemContext(ctx context.Context, b model.ControlBatch, change model.ControlChange) context.Context {
	known := map[string]bool{}
	for ref, allowed := range knownEvidenceReferences(ctx) {
		known[ref] = allowed
	}
	if b.ID != "" {
		known[b.ID] = true
	}
	ctx = context.WithValue(ctx, knownEvidenceKey{}, known)
	if s.Data == nil || change.RecordID == "" || change.ConnectionID == "" {
		return ctx
	}
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if b.Source == "dws" {
		return s.enrichDWSItemContext(lookup, ctx, b, change)
	}
	scope := f.Scope{OwnerID: b.OwnerID}
	catalog, err := s.Data.Catalog(lookup, scope)
	if err != nil {
		return ctx
	}
	fields := []string{}
	for _, set := range catalog {
		if set.Name == "platform_records" {
			for _, field := range set.Fields {
				fields = append(fields, field.Name)
			}
		}
	}
	// Host snapshots stay bounded even as the authorized catalog grows.
	if len(fields) > 32 {
		fields = fields[:32]
	}
	if len(fields) == 0 {
		return ctx
	}
	query := f.Query{Dataset: "platform_records", Fields: fields, Filters: []f.Filter{{Field: "id", Operator: "eq", Value: change.RecordID}, {Field: "connection_id", Operator: "eq", Value: change.ConnectionID}}, Limit: 1}
	result, err := s.Data.Query(lookup, scope, query)
	if err != nil || len(result.Rows) != 1 {
		return ctx
	}
	record := result.Rows[0]
	if record["id"] != change.RecordID || record["connection_id"] != change.ConnectionID || record["source"] != change.Source {
		return ctx
	}
	facts := map[string]any{"record": record, "field_catalog": catalog, "checked_at": time.Now().UTC(), "record_reference": "platform_records.id"}
	// Structured fields are resolved by the host from persisted evidence links.
	// Keep the original record alongside them so missing source data stays visible.
	for _, dataset := range catalog {
		if dataset.Name != "academic_items" {
			continue
		}
		q := f.Query{Dataset: "academic_items", Fields: []string{"id", "connection_id", "offering_id", "course_ref", "course_name", "kind", "task_type", "title", "text", "author", "metadata", "deadline", "published_time", "deadline_time", "start_time", "end_time", "status", "source_status", "submission_status", "used_try_count", "user_score", "evaluate_start", "evaluate_end", "chapter_id", "lesson_id", "unit_id", "source_term_id", "path", "missing", "record_ids", "body_status", "coverage"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "record_ids", Operator: "contains", Value: "\"" + change.RecordID + "\""}}, Limit: 2}
		if structured, e := s.Data.Query(lookup, scope, q); e == nil && len(structured.Rows) == 1 && academicItemMatchesChange(structured.Rows[0], change) {
			item := structured.Rows[0]
			facts["academic_item"] = item
			facts["preferred_query_dataset"] = "academic_items"
			if id, ok := item["id"].(string); ok && isAcademicItemID(id) {
				ctx = context.WithValue(ctx, knownAcademicEvidenceKey{}, academicEvidenceReferences{OwnerID: b.OwnerID, ConnectionID: change.ConnectionID, RecordID: change.RecordID, IDs: map[string]bool{id: true}})
			}
		}
	}
	var course map[string]any
	if record["kind"] == "course" {
		course = record
	} else if parent, ok := record["parent_id"].(string); ok && parent != "" {
		q := f.Query{Dataset: "platform_records", Fields: []string{"id", "connection_id", "source", "kind", "external_id", "title"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "kind", Operator: "eq", Value: "course"}, {Field: "external_id", Operator: "eq", Value: parent}}, Limit: 2}
		page, e := s.Data.Query(lookup, scope, q)
		if e == nil && len(page.Rows) == 1 {
			c := page.Rows[0]
			if c["connection_id"] == change.ConnectionID && c["source"] == change.Source && c["kind"] == "course" && c["external_id"] == parent {
				course = c
			}
		}
	}
	if course == nil && record["kind"] == "notice" {
		if matched, method := s.noticeCourse(lookup, scope, change, record); matched != nil {
			course = matched
			facts["course_link_method"] = method
			facts["course_link_source"] = map[string]any{"record_id": change.RecordID, "title": record["title"], "course_name": record["course_name"]}
		}
	}
	workflows := map[string]any{"complete": false, "items": []any{}}
	if course != nil {
		refs := map[string]bool{}
		for ref, allowed := range knownEvidenceReferences(ctx) {
			refs[ref] = allowed
		}
		for _, key := range []string{"id", "external_id"} {
			if value, ok := course[key].(string); ok && value != "" {
				refs[value] = true
			}
		}
		ctx = context.WithValue(ctx, knownEvidenceKey{}, refs)
		facts["course"] = course
		ref, _ := course["external_id"].(string)
		for _, dataset := range catalog {
			if dataset.Name != "academic_offerings" {
				continue
			}
			q := f.Query{Dataset: "academic_offerings", Fields: []string{"id", "course_ref", "course_name", "native_course_id", "native_class_id", "native_term_id", "course_type", "teacher", "school", "description", "details", "source_terms", "coverage"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "course_ref", Operator: "eq", Value: ref}}, Limit: 2}
			if page, e := s.Data.Query(lookup, scope, q); e == nil && len(page.Rows) == 1 {
				facts["academic_offering"] = page.Rows[0]
			}
		}
		name, _ := course["title"].(string)
		entityID := "source=" + change.Source + "|connection=" + change.ConnectionID + "|course_ref=" + ref
		facts["entity"] = map[string]string{"kind": "course", "id": entityID, "name": name}
		summaries := []map[string]any{}
		after := ""
		complete := false
		for pages := 0; pages < 20; pages++ {
			items, e := s.Agents.Registry.Repository.ListWorkflows(lookup, b.OwnerID, 100, after)
			if e != nil {
				break
			}
			for _, w := range items {
				var d struct {
					Entity struct {
						Kind string `json:"kind"`
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"entity"`
					Goal string `json:"goal"`
				}
				if w.OwnerID != b.OwnerID || w.Status != "active" || json.Unmarshal(w.Definition, &d) != nil || d.Entity.Kind != "course" || d.Entity.ID != entityID {
					continue
				}
				summaries = append(summaries, map[string]any{"workflow_id": w.ID, "version": w.Version, "status": w.Status, "name": w.Name, "entity": d.Entity, "goal": d.Goal})
			}
			if len(items) < 100 {
				complete = true
				break
			}
			next := items[len(items)-1].ID
			if next == "" || next == after {
				break
			}
			after = next
		}
		workflows = map[string]any{"complete": complete, "entity_id": entityID, "items": summaries}
	}
	raw, err := json.Marshal(facts)
	if err != nil || len(raw) > 48<<10 {
		return ctx
	}
	summary, err := json.Marshal(workflows)
	if err != nil || len(summary) > 16<<10 {
		return f.WithHostEvidence(ctx, b.OwnerID, raw, nil)
	}
	return f.WithHostEvidence(ctx, b.OwnerID, raw, summary)
}

// DWS native message IDs are unique only within their connection and group.
func (s *Service) enrichDWSItemContext(lookup, ctx context.Context, b model.ControlBatch, change model.ControlChange) context.Context {
	group := dwsMessageGroup(change)
	if change.Resource != "message" || group == "" {
		return ctx
	}
	query := f.Query{Dataset: "dws_messages", Fields: []string{"id", "connection_id", "source", "group_id", "group_name", "external_id", "text", "sender", "create_time_raw", "update_time_raw"}, Filters: []f.Filter{{Field: "external_id", Operator: "eq", Value: change.RecordID}, {Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "group_id", Operator: "eq", Value: group}}, Limit: 2}
	page, err := s.Data.Query(lookup, f.Scope{OwnerID: b.OwnerID}, query)
	if err != nil || len(page.Rows) != 1 {
		return ctx
	}
	record := page.Rows[0]
	if record["external_id"] != change.RecordID || record["connection_id"] != change.ConnectionID || record["group_id"] != group || record["source"] != "dws" {
		return ctx
	}
	refs := knownEvidenceReferences(ctx)
	if id, ok := record["id"].(string); ok && id != "" {
		refs[id] = true
	}
	raw, err := json.Marshal(map[string]any{"record": record, "record_reference": "dws_messages.external_id", "checked_at": time.Now().UTC()})
	if err != nil || len(raw) > 48<<10 {
		return ctx
	}
	return f.WithHostEvidence(ctx, b.OwnerID, raw, nil)
}

// normalizedCourseTitle removes only presentation differences, never words.
func normalizedCourseTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		if r >= '\uff01' && r <= '\uff5e' {
			return r - 0xfee0
		}
		return r
	}, value)
}

func (s *Service) noticeCourse(ctx context.Context, scope f.Scope, change model.ControlChange, record map[string]any) (map[string]any, string) {
	name, _ := record["course_name"].(string)
	method := "explicit_course_name"
	if strings.TrimSpace(name) == "" {
		title, _ := record["title"].(string)
		start := strings.Index(title, "《")
		if start < 0 {
			return nil, ""
		}
		rest := title[start+len("《"):]
		end := strings.Index(rest, "》")
		if end < 0 {
			return nil, ""
		}
		name, method = rest[:end], "explicit_notice_title"
	}
	wanted := normalizedCourseTitle(name)
	if wanted == "" {
		return nil, ""
	}
	var match map[string]any
	// Authorized Query has a maximum offset of 10000. An incomplete scan cannot
	// establish uniqueness, so reaching the bound fails closed.
	for offset := 0; offset <= 10000; offset += 100 {
		q := f.Query{Dataset: "platform_records", Fields: []string{"id", "connection_id", "source", "kind", "external_id", "title"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: change.ConnectionID}, {Field: "kind", Operator: "eq", Value: "course"}}, Limit: 100, Offset: offset}
		page, err := s.Data.Query(ctx, scope, q)
		if err != nil {
			return nil, ""
		}
		for _, candidate := range page.Rows {
			title, _ := candidate["title"].(string)
			if candidate["connection_id"] != change.ConnectionID || candidate["source"] != change.Source || candidate["kind"] != "course" || normalizedCourseTitle(title) != wanted {
				continue
			}
			ref, _ := candidate["external_id"].(string)
			id, _ := candidate["id"].(string)
			if ref == "" || id == "" {
				return nil, ""
			}
			if match != nil {
				return nil, ""
			} // Includes different teaching classes.
			match = candidate
		}
		if len(page.Rows) < 100 {
			return match, method
		}
	}
	return nil, ""
}
