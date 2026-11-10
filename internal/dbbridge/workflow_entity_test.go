package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func mustEntityDefinition(id string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"entity": map[string]string{"kind": "task", "id": id, "name": "测试事项" + id}})
	return raw
}
func TestConcurrentEntityWorkflowCreateOnlyOneActive(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "entity-owner", model.RoleUser)
	repo := db.Agents()
	a, e := (f.Registry{Repository: repo}).Create(ctx, u.ID, definition("learner", "mock", nil, nil))
	must(t, e)
	scope := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.SaveWorkflow(ctx, scope, f.Workflow{Name: "工科数学分析", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"chaoxing/connection/course/class","name":"工科数学分析"}}`)}, true)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if f.SafeError(err).Code == "CONFLICT" && strings.Contains(f.SafeError(err).Message, "workflow_get") {
				conflicts++
			} else {
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	var count int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1 AND status='active'`, u.ID).Scan(&count))
	if count != 1 {
		t.Fatal(count)
	}
	_, e = repo.SaveWorkflow(ctx, scope, f.Workflow{Name: "工科数学分析实验报告", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"chaoxing/connection/course/class","name":"工科数学分析"},"goal":"核查该课实验报告要求"}`)}, true)
	must(t, e)
}
