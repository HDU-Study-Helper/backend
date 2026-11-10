package mooc

import (
	"academic/backend/internal/contract"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestNoticeRequiresContentStringAndAllowsExplicitEmpty(t *testing.T) {
	co := course{ID: "10", Term: "101", Type: 1}
	for _, content := range []any{nil, json.Number("1"), map[string]any{}, []any{}} {
		if _, e := noticeRecords([]any{map[string]any{"id": "1", "content": content}}, co); !errors.Is(e, contract.ErrParse) {
			t.Fatal("missing/nonstring notice accepted", content, e)
		}
	}
	rows, e := noticeRecords([]any{map[string]any{"id": "1", "content": ""}}, co)
	if e != nil || len(rows) != 1 || rows[0].Fields["body_empty_source"] != true {
		t.Fatal(rows, e)
	}
}
func TestDiscussionRequiresIdentityAndContentString(t *testing.T) {
	for _, tc := range []struct {
		post  string
		valid bool
	}{{`{id:14}`, false}, {`{id:14,content:null}`, false}, {`{content:""}`, false}, {`{id:"invalid",content:""}`, false}, {`{id:14,content:""}`, true}} {
		a := testAdapter(func(*http.Request) (*http.Response, error) {
			return response(`dwr.engine._remoteHandleCallback('0','0',{unitId:13,post:` + tc.post + `});`), nil
		})
		_, h, _ := a.restore(scope, sessionFor(t, a, scope, "fixture"))
		co := course{ID: "10", Term: "101", Type: 1}
		row := contract.Record{ParentID: co.ref(), Fields: map[string]any{"sourceTermId": "101", "source_collection": "units", "contentType": "6", "contentId": "14", "unitId": "13"}}
		e := enrichUnit(context.Background(), h, co, &row)
		if tc.valid {
			if e != nil || row.Fields["body_empty_source"] != true {
				t.Fatal(tc, e, row)
			}
		} else if !errors.Is(e, contract.ErrParse) {
			t.Fatal("missing content/id accepted", tc, e)
		}
	}
}
