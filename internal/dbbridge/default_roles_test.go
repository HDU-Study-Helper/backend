package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"strings"
	"testing"
)

func TestApplicationDefaultRoles(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := fixtureUser(t, db, "existing", model.RoleAdmin)
	r := f.Registry{Repository: db.Agents()}
	d := definition("custom learner", "mock", nil, nil)
	d.Type = "learner"
	a, e := r.Create(ctx, old.ID, d)
	must(t, e)
	must(t, db.EnableDefaultRoles(ctx))
	fresh := fixtureUser(t, db, "new", model.RoleUser)
	must(t, db.EnableDefaultRoles(ctx))
	for _, u := range []model.User{old, fresh} {
		agents, e := r.Repository.ListAgents(ctx, u.ID, 10, "")
		must(t, e)
		by := map[string]f.AgentInstance{}
		for _, v := range agents {
			by[v.Type] = v
		}
		if len(agents) != 5 || len(by) != 5 || !strings.Contains(by["chat"].SystemPrompt, by["worker"].ID) || !strings.Contains(by["chat"].SystemPrompt, by["learner"].ID) || !strings.Contains(by["filter"].SystemPrompt, by["worker"].ID) || !strings.Contains(by["worker"].SystemPrompt, by["learner"].ID) || strings.Contains(by["filter"].SystemPrompt, "{{") {
			t.Fatal("incomplete or incorrect dispatch binding")
		}
		if u == old && (by["learner"].ID != a.ID || by["learner"].SystemPrompt != a.SystemPrompt || by["learner"].Model.Provider != "mock") {
			t.Fatal("existing configuration overwritten")
		}
		if by["worker"].Model.Mode != "default" || by["worker"].Model.Provider != "@configured" {
			t.Fatal("unexpected provider binding")
		}
	}
}
