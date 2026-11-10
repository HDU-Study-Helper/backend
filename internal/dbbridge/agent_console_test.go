package dbbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/agentadmin"
	"academic/backend/internal/credential"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

func TestAgentConsoleManagementAndEncryptedSettings(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	admin := fixtureUser(t, db, "console-admin", model.RoleAdmin)
	other := fixtureUser(t, db, "console-other", model.RoleUser)
	vault, err := credential.New(bytes.Repeat([]byte{7}, 32))
	must(t, err)
	console, err := agentadmin.New(ctx, db.Agents(), db.AgentAdmin(), db.Users(), vault, true, db.AgentData())
	must(t, err)
	request := func(user model.User, path, method string, body any, want int) map[string]json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if user.ID != "" {
			req = req.WithContext(httpkit.WithUser(req.Context(), user))
		}
		res := httptest.NewRecorder()
		console.Routes().ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.Code, want, res.Body.String())
		}
		var result map[string]json.RawMessage
		if res.Code != 204 {
			must(t, json.Unmarshal(res.Body.Bytes(), &result))
		}
		return result
	}
	request(model.User{}, "/settings", "GET", nil, 401)
	request(other, "/settings", "GET", nil, 403)
	cfg := console.Settings.View()
	cfg.Providers[0].APIKey = "console-test-private-key"
	settings := request(admin, "/settings", "PUT", cfg, 200)
	if bytes.Contains(settings["providers"], []byte("console-test-private-key")) {
		t.Fatal("key returned to browser")
	}
	var encrypted []byte
	must(t, db.pool.QueryRow(ctx, `SELECT encrypted_settings FROM agent_framework_settings WHERE id=1`).Scan(&encrypted))
	if bytes.Contains(encrypted, []byte("console-test-private-key")) {
		t.Fatal("plaintext key in postgres")
	}
	reloaded, err := agentadmin.NewSettings(ctx, db.AgentAdmin(), vault, false)
	must(t, err)
	if !reloaded.View().Providers[0].HasAPIKey {
		t.Fatal("settings restart persistence")
	}
	body := map[string]any{"name": "console agent", "agent_type": "demo", "system_prompt": "test", "model_config": map[string]string{"provider": "demo", "model": "local-demo"}, "enabled_tools": []string{"memory_list"}, "permissions": []string{"memory.read"}, "status": "enabled"}
	created := request(admin, "/instances", "POST", body, 201)
	var agentID string
	must(t, json.Unmarshal(created["agent_id"], &agentID))
	path := "/instances/" + agentID
	body["name"] = "updated"
	request(admin, path, "PUT", body, 200)
	request(admin, path+"?owner_id="+other.ID, "PUT", body, 404)
	memory := request(admin, path+"/memories", "POST", map[string]any{"title": "structured", "content": `{"language":"中文"}`, "category": "preference", "priority": 10}, 201)
	var memoryID string
	must(t, json.Unmarshal(memory["memory_id"], &memoryID))
	request(admin, path+"/memories?owner_id="+other.ID, "GET", nil, 404)
	run := request(admin, path+"/runs", "POST", map[string]any{"input": map[string]string{"text": "demo"}}, 200)
	var result f.RunResult
	must(t, json.Unmarshal(run["result"], &result))
	if !bytes.Contains([]byte(result.Output), []byte("structured")) {
		t.Fatal("memory not returned by actual tool")
	}
	record, err := db.Agents().GetRun(ctx, admin.ID, result.RunID)
	must(t, err)
	if record.ModelCalls != 2 || record.ToolCalls != 1 || record.Status != "succeeded" {
		t.Fatal(record)
	}
	listed := request(admin, path+"/runs", "GET", nil, 200)
	var runs []f.RunRecord
	must(t, json.Unmarshal(listed["items"], &runs))
	if len(runs) != 1 {
		t.Fatal("run history missing")
	}
	request(admin, path+"/memories/"+memoryID, "DELETE", nil, 204)
	// Ownership cannot be selected in a JSON body.
	request(admin, path+"/runs", "POST", map[string]any{"input": map[string]string{"text": "demo"}, "owner_user_id": other.ID}, 400)
	request(admin, path+"/runs", "POST", map[string]any{"input": map[string]string{"text": "demo"}}, http.StatusOK)
}

func TestAgentConsoleHotProviderUsesSavedKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "provider-admin", model.RoleAdmin)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-hot-key" {
			t.Error("saved endpoint or key was not used")
		}
		var body struct {
			Model string `json:"model"`
		}
		must(t, json.NewDecoder(r.Body).Decode(&body))
		if body.Model != "fixture-model" {
			t.Error("wrong model")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"fixture model response"},"finish_reason":"stop"}],"usage":{"total_tokens":12}}`))
	}))
	defer server.Close()
	vault, err := credential.New(bytes.Repeat([]byte{9}, 32))
	must(t, err)
	console, err := agentadmin.New(ctx, db.Agents(), db.AgentAdmin(), db.Users(), vault, false, db.AgentData())
	must(t, err)
	cfg := console.Settings.View()
	cfg.Providers[0].BaseURL = server.URL + "/v1"
	cfg.Providers[0].APIKey = "fixture-hot-key"
	_, err = console.Settings.Save(ctx, cfg)
	must(t, err)
	a, err := console.Service.Registry.Create(ctx, u.ID, f.AgentDefinition{Name: "actual provider path", SystemPrompt: "test", Model: f.ModelConfig{Provider: "default", Model: "fixture-model"}})
	must(t, err)
	result, err := console.Service.Run(ctx, u.ID, f.RunRequest{AgentID: a.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{"text":"test"}`)})
	must(t, err)
	if result.Output != "fixture model response" {
		t.Fatal(result)
	}
	record, err := db.Agents().GetRun(ctx, u.ID, result.RunID)
	must(t, err)
	if record.Usage.TotalTokens != 12 {
		t.Fatal("provider usage not saved")
	}
}

func TestAgentConsoleMemoryV02WorkflowAndPatch(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	admin := fixtureUser(t, db, "v02-admin", model.RoleAdmin)
	other := fixtureUser(t, db, "v02-other", model.RoleUser)
	vault, err := credential.New(bytes.Repeat([]byte{8}, 32))
	must(t, err)
	console, err := agentadmin.New(ctx, db.Agents(), db.AgentAdmin(), db.Users(), vault, true, db.AgentData())
	must(t, err)
	a, err := console.Service.Registry.Create(ctx, admin.ID, definition("memory console", "demo", []string{"memory_list"}, []string{"memory.read"}))
	must(t, err)
	scope := f.Scope{OwnerID: admin.ID, AgentID: a.ID}
	w, err := db.Agents().SaveWorkflow(ctx, scope, f.Workflow{Name: "UI context", Definition: json.RawMessage(`{}`)}, true)
	must(t, err)
	oa, err := console.Service.Registry.Create(ctx, other.ID, definition("other", "demo", nil, nil))
	must(t, err)
	foreign, err := db.Agents().SaveWorkflow(ctx, f.Scope{OwnerID: other.ID, AgentID: oa.ID}, f.Workflow{Name: "foreign", Definition: json.RawMessage(`{}`)}, true)
	must(t, err)
	request := func(path, method string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(httpkit.WithUser(req.Context(), admin))
		res := httptest.NewRecorder()
		console.Routes().ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s status %d want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		return res.Body.Bytes()
	}
	base := "/instances/" + a.ID + "/memories"
	raw := request(base, "POST", map[string]any{"title": "public", "content": "content", "visibility": "PUB", "memory_type": "TEMP", "is_personal": true, "level": "L2", "structured_content": map[string]any{"verified": true}, "source_refs": []any{map[string]any{"source": "fixture"}}}, 201)
	var public f.Memory
	must(t, json.Unmarshal(raw, &public))
	raw = request(base+"/"+public.ID, "PUT", map[string]any{"title": "updated legacy editor", "content": "changed"}, 200)
	var patched f.Memory
	must(t, json.Unmarshal(raw, &patched))
	if patched.Visibility != "PUB" || patched.MemoryType != "TEMP" || !patched.IsPersonal || patched.Level != "L2" || len(patched.StructuredContent) == 0 {
		t.Fatal("legacy editor reset V0.2 classification")
	}
	request(base, "POST", map[string]any{"title": "forged owner", "content": "bad", "owner_user_id": other.ID}, 400)
	request(base, "POST", map[string]any{"title": "forged workflow", "content": "bad", "owner_workflow_id": w.ID}, 403)
	raw = request(base+"?workflow_id="+w.ID, "POST", map[string]any{"title": "workflow", "content": "workflow content", "owner_workflow_id": w.ID}, 201)
	var wm f.Memory
	must(t, json.Unmarshal(raw, &wm))
	raw = request(base, "GET", nil, 200)
	if bytes.Contains(raw, []byte(wm.ID)) {
		t.Fatal("Workflow private memory leaked without context")
	}
	raw = request(base+"?workflow_id="+w.ID, "GET", nil, 200)
	if !bytes.Contains(raw, []byte(wm.ID)) {
		t.Fatal("Workflow private memory absent from authorized context")
	}
	request(base+"?workflow_id="+foreign.ID, "GET", nil, 404)
	request(base+"/"+public.ID+"/relations?workflow_id="+w.ID, "POST", map[string]any{"target_memory_id": wm.ID, "relation_type": "RELATED_TO"}, 201)
	raw = request(base+"/"+public.ID+"/relations", "GET", nil, 200)
	if bytes.Contains(raw, []byte(wm.ID)) {
		t.Fatal("HTTP relation leaked private Workflow endpoint")
	}
	raw = request(base+"/"+public.ID+"/relations?workflow_id="+w.ID, "GET", nil, 200)
	if !bytes.Contains(raw, []byte(wm.ID)) {
		t.Fatal("authorized relation missing")
	}
	request("/instances/"+a.ID+"/runs?workflow_id="+w.ID, "POST", map[string]any{"input": map[string]any{"task": "list current memory"}}, 200)
	request("/workflows", "GET", nil, 200)
}
