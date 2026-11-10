package mooc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestAllResourcesSplitAtPlatformPageLimit(t *testing.T) {
	for _, kind := range []contract.Resource{contract.Notice, "chapter"} {
		t.Run(string(kind), func(t *testing.T) {
			calls := 0
			a := testAdapter(func(req *http.Request) (*http.Response, error) {
				calls++
				if kind == contract.Notice {
					if !strings.Contains(req.URL.Path, "getAllAnnouncementByTerm") {
						t.Fatal("wrong notice read")
					}
					var b strings.Builder
					refs := []string{}
					for i := 1; i <= 501; i++ {
						fmt.Fprintf(&b, `var s%d={};s%d.id=%d;s%d.content="";`, i, i, i, i)
						refs = append(refs, fmt.Sprintf("s%d", i))
					}
					b.WriteString(`dwr.engine._remoteHandleCallback('0','0',[` + strings.Join(refs, ",") + `]);`)
					return response(b.String()), nil
				}
				rows := []map[string]any{}
				for i := 1; i <= 501; i++ {
					rows = append(rows, map[string]any{"id": i, "name": "Chapter"})
				}
				b, _ := json.Marshal(map[string]any{"code": 0, "result": map[string]any{"mocTermDto": map[string]any{"id": 101, "chapters": rows}}})
				return response(string(b)), nil
			})
			s := sessionFor(t, a, scope, "fixture")
			c, _ := newCursor(scope, kind, "")
			c.Done = true
			c.Courses = []course{{ID: "10", Term: "101", Type: 1, Name: "Owner"}}
			b, _ := json.Marshal(c)
			raw := base64Cursor(b)
			seen := map[string]bool{}
			for i := 0; i < 2; i++ {
				p, e := a.Sync(context.Background(), scope, s, kind, raw)
				if e != nil {
					t.Fatal(e)
				}
				want := 500
				if i == 1 {
					want = 1
				}
				if len(p.Records) != want || p.HasMore != (i == 0) {
					t.Fatal("incorrect split", len(p.Records), p.HasMore)
				}
				for _, row := range p.Records {
					if seen[row.ExternalID] {
						t.Fatal("duplicate record")
					}
					seen[row.ExternalID] = true
				}
				if i == 0 && (p.NextCursor == "" || p.NextCursor == raw) {
					t.Fatal("cursor failed to advance")
				}
				raw = p.NextCursor
				s = p.Session
			}
			if len(seen) != 501 || calls != 1 {
				t.Fatal("records dropped or overflow repeated network", len(seen), calls)
			}
		})
	}
}
