package dbbridge

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"academic/backend/internal/model"
)

func TestMOOCTaskStateProjectionPreservesExplicitCatalogEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status, participation string
		view, replies, completed    any
		personalReplies             any
	}{
		{"green", "completed", "participated", 5, 1, true, 1},
		{"gray", "incomplete", "unknown", 0, nil, false, nil},
		{"explicit-zero-replies", "completed", "not_participated", 5, 0, true, 0},
		{"unknown", "unknown", "unknown", nil, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{
				"courseId": "course", "termId": "route", "sourceTermId": "source", "source_scope": "active_source",
				"viewStatus": tc.view, "replyCount": tc.replies,
				"task_point_status": tc.status, "task_point_status_source": "catalog.unit.viewStatus",
				"task_point_status_semantics": "server_returned_official_directory_learned_marker",
				"participation_status":        tc.participation, "participation_status_reason": "catalog.unit.replyCount",
				"reply_collection_policy": "disabled_current_user_task_state_only", "cookie": "PRIVATE_COOKIE",
				"native_progress_fields":        map[string]any{"viewStatus": tc.view, "replyCount": tc.replies, "cookie": "PRIVATE_COOKIE"},
				"detail_native_progress_fields": map[string]any{"replyCount": nil, "viewStatus": nil, "cookie": "PRIVATE_COOKIE"},
				"detail_progress_fields":        map[string]any{"replyCount": nil, "viewStatus": nil, "cookie": "PRIVATE_COOKIE"},
			}
			if tc.completed != nil {
				fields["task_point_completed"] = tc.completed
			}
			if tc.personalReplies != nil {
				fields["current_user_reply_count"] = tc.personalReplies
			}
			p, err := buildAcademicProjection("owner", "connection", "mooc", []model.PlatformRecord{
				academicFixture(1, "mooc", "discussion", "topic", "", "Topic", fields),
			})
			must(t, err)
			if len(p.items) != 1 {
				t.Fatalf("expected one discussion: %+v", p.items)
			}
			item := p.items[0]
			personal, metadata := map[string]any{}, map[string]any{}
			must(t, json.Unmarshal(item.PersonalState, &personal))
			must(t, json.Unmarshal(item.Metadata, &metadata))
			for _, projected := range []map[string]any{personal, metadata} {
				for _, key := range []string{"task_point_status", "task_point_status_source", "participation_status", "participation_status_reason"} {
					if projected[key] != fields[key] {
						t.Fatalf("lost explicit %s: %+v", key, projected)
					}
				}
				value, exists := projected["task_point_completed"]
				if exists != (tc.completed != nil) || value != tc.completed {
					t.Fatalf("completion false/absence changed: %+v", projected)
				}
				value, exists = projected["current_user_reply_count"]
				if exists != (tc.personalReplies != nil) {
					t.Fatalf("unknown personal reply count fabricated: %+v", projected)
				}
				if exists && value != float64(tc.personalReplies.(int)) {
					t.Fatalf("personal reply count changed: %+v", projected)
				}
			}
			if metadata["task_point_status_semantics"] != fields["task_point_status_semantics"] || metadata["reply_collection_policy"] != fields["reply_collection_policy"] || metadata["source_scope"] != "active_source" || item.SourceTermID != "source" || item.NativeTermID != "route" {
				t.Fatalf("source or task-marker semantics lost: %+v", item)
			}
			for _, scope := range []string{"native_progress_fields", "detail_native_progress_fields", "detail_progress_fields"} {
				native := metadata[scope].(map[string]any)
				for _, key := range []string{"viewStatus", "replyCount"} {
					if _, exists := native[key]; !exists {
						t.Fatalf("explicit raw null dropped from %s: %+v", scope, metadata)
					}
				}
				if scope == "native_progress_fields" && (!reflect.DeepEqual(native["viewStatus"], personal["viewStatus"]) || !reflect.DeepEqual(native["replyCount"], personal["replyCount"])) {
					t.Fatalf("raw catalog evidence changed: %+v", metadata)
				}
			}
			if item.Status != "unknown" || strings.Contains(string(item.PersonalState)+string(item.Metadata), "PRIVATE_COOKIE") {
				t.Fatalf("generic status inferred or credential escaped: %+v", item)
			}
		})
	}
}

func TestMOOCTaskStateProjectionDoesNotInferFromRosterOrMergeSourceTerms(t *testing.T) {
	encode := func(source string) string {
		return base64.RawURLEncoding.EncodeToString(academicJSON([]string{"course", "route", source, "unit", "content", "node"}))
	}
	rows := []model.PlatformRecord{}
	for i, source := range []string{"route", "reference"} {
		rows = append(rows, academicFixture(int64(i+1), "mooc", "discussion", encode(source), "", "Same topic", map[string]any{
			"courseId": "course", "termId": "route", "sourceTermId": source,
			"viewStatus": 5, "replyCount": nil, "countReply": 73,
			"native_progress_fields": map[string]any{"viewStatus": 5, "replyCount": nil},
		}))
	}
	rows = append(rows, academicFixture(3, "mooc", "discussion_reply_user", "reply", encode("reference"), "", map[string]any{
		"is_current_user": true, "participation_status": "reply_observed", "task_point_completed": true,
	}))
	p, err := buildAcademicProjection("owner", "connection", "mooc", rows)
	must(t, err)
	if len(p.items) != 3 {
		t.Fatalf("source terms or roster merged: %+v", p.items)
	}
	for _, item := range p.items {
		if item.Kind != "discussion" {
			continue
		}
		personal := map[string]any{}
		must(t, json.Unmarshal(item.PersonalState, &personal))
		for _, key := range []string{"task_point_status", "task_point_completed", "participation_status", "current_user_reply_count"} {
			if _, exists := personal[key]; exists {
				t.Fatalf("%s fabricated from raw enum, global reply count, or roster: %+v", key, personal)
			}
		}
		if item.Status != "unknown" || len(item.RecordIDs) != 1 {
			t.Fatalf("unrelated evidence changed task: %+v", item)
		}
	}
	other, err := buildAcademicProjection("other-owner", "connection", "mooc", rows)
	must(t, err)
	for _, item := range p.items {
		for _, foreign := range other.items {
			if item.ID == foreign.ID {
				t.Fatal("projection identity merged owners")
			}
		}
	}
}

func TestMOOCTaskStateAgentMetadataAllowlist(t *testing.T) {
	projection := safeMetadataProjection("a.metadata")
	for _, key := range []string{"task_point_completed", "task_point_status", "task_point_status_source", "task_point_status_semantics", "participation_status", "participation_status_reason", "current_user_reply_count", "reply_collection_policy"} {
		if !strings.Contains(projection, "a.metadata->'"+key+"'") {
			t.Fatalf("agent metadata hides %s", key)
		}
	}
	for _, scope := range []string{"native_progress_fields", "current_user_participation_fields", "detail_native_progress_fields", "detail_progress_fields"} {
		for _, key := range []string{"viewStatus", "replyCount"} {
			if !strings.Contains(projection, "jsonb_typeof((a.metadata->'"+scope+"')->'"+key+"')='null'") {
				t.Fatalf("agent metadata drops explicit null %s.%s", scope, key)
			}
		}
	}
	for _, key := range []string{"cookie", "token", "authorization", "credentials"} {
		if strings.Contains(projection, "->'"+key+"'") {
			t.Fatalf("agent metadata allows private %s", key)
		}
	}
	malicious := academicSafeFields(map[string]any{
		"task_point_status_source": map[string]any{"cookie": "PRIVATE_COOKIE"},
		"task_point_completed":     false, "current_user_reply_count": nil,
	}, append(append([]string{}, academicPersonalKeys...), academicMetadataKeys...))
	if _, exists := malicious["task_point_status_source"]; exists || malicious["task_point_completed"] != false {
		t.Fatalf("unsafe source object accepted or false removed: %+v", malicious)
	}
	if value, exists := malicious["current_user_reply_count"]; !exists || value != nil {
		t.Fatalf("explicit unknown reply count changed: %+v", malicious)
	}
}
