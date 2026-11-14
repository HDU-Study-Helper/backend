//go:build ignore

// One-off read-only data_query usability probe. At most two real model requests;
// no Runtime run, Agent update, migration, Workflow/Memory writes or collectors.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agentadmin"
	"academic/backend/internal/credential"
	"academic/backend/internal/dbbridge"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("invalid database configuration")
	}
	cfg.RuntimeParams["search_path"] = "public"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("read-only database unavailable")
	}
	defer conn.Close(context.Background())
	var schema string
	if err = conn.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil || schema != "public" {
		return errors.New("unexpected source schema")
	}
	rows, err := conn.Query(ctx, `SELECT config FROM agent_instances WHERE status='enabled' AND config->>'agent_type'='learner' ORDER BY agent_id LIMIT 2`)
	if err != nil {
		return errors.New("cannot read learner configuration")
	}
	agents := []f.AgentInstance{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var definition f.AgentDefinition
		if json.Unmarshal(raw, &definition) != nil {
			rows.Close()
			return errors.New("invalid agent config")
		}
		agents = append(agents, f.AgentInstance{AgentDefinition: definition})
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(agents) != 1 {
		return errors.New("one enabled Learner configuration required")
	}
	var owner, connection, offering string
	if err = conn.QueryRow(ctx, `SELECT a.user_id,a.connection_id,a.id FROM academic_offerings a JOIN users u ON u.id=a.user_id JOIN platform_connections c ON(c.user_id,c.id)=(a.user_id,a.connection_id) WHERE NOT u.disabled AND c.enabled AND c.state='active' ORDER BY a.id LIMIT 1`).Scan(&owner, &connection, &offering); err != nil {
		return errors.New("no active structured course available")
	}
	// ConnString retains parse-time text; explicitly serialize read-only parameters.
	endpoint, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || (endpoint.Scheme != "postgres" && endpoint.Scheme != "postgresql") {
		return errors.New("probe expects a postgres URI")
	}
	parameters := endpoint.Query()
	parameters.Set("default_transaction_read_only", "on")
	parameters.Set("search_path", "public")
	endpoint.RawQuery = parameters.Encode()
	readonlyDSN := endpoint.String()
	db, err := dbbridge.Open(ctx, readonlyDSN)
	if err != nil {
		return errors.New("readonly repository unavailable")
	}
	defer db.Close()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if err != nil {
		return errors.New("credential key unavailable")
	}
	defer clear(key)
	vault, err := credential.New(key)
	if err != nil {
		return err
	}
	settings, err := agentadmin.NewSettings(ctx, db.AgentAdmin(), vault, false)
	if err != nil {
		return err
	}
	provider, model, err := settings.ResolveModel(agents[0].Model)
	if err != nil {
		return err
	}
	model.MaxTokens = 2048
	query := f.Query{Dataset: "academic_items", Fields: []string{"id", "connection_id", "offering_id", "title", "kind"}, Filters: []f.Filter{{Field: "connection_id", Operator: "eq", Value: connection}, {Field: "offering_id", Operator: "eq", Value: offering}}, Limit: 2}
	specRegistry := f.NewToolRegistry()
	if err = (f.Builtins{Data: db.AgentData()}).Register(specRegistry); err != nil {
		return err
	}
	var spec f.ToolSpec
	for _, candidate := range specRegistry.Catalog() {
		if candidate.Name == "data_query" {
			spec = candidate
		}
	}
	dir, err := os.MkdirTemp("../.build", "learner-single-tool-")
	if err != nil {
		return err
	}
	_ = os.Chmod(dir, 0700)
	save := func(name string, v any) error {
		raw, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(dir, name), raw, 0600)
	}
	expected, _ := json.Marshal(query)
	request := f.ModelRequest{Config: model, Tools: []f.ToolSpec{spec}, Messages: []f.Message{{Role: "system", Content: "You are testing a read-only data query tool. Execute exactly the requested query once using the provided tool, then briefly report rows and whether more data is available. Do not fetch further pages. Do not invent IDs."}, {Role: "user", Content: "Call data_query once with this request: " + string(expected)}}}
	if err = save("request-1.private.json", request); err != nil {
		return err
	}
	first, err := provider.Complete(ctx, request)
	_ = save("response-1.private.json", map[string]any{"response": first, "error": f.SafeError(err)})
	if err != nil {
		return err
	}
	if len(first.Message.ToolCalls) != 1 {
		return errors.New("model did not return exactly one tool call")
	}
	call := first.Message.ToolCalls[0]
	if call.Function.Name != "data_query" {
		return errors.New("unexpected tool")
	}
	var arguments any
	if json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil {
		return errors.New("invalid model JSON")
	}
	var schemaObject any
	_ = json.Unmarshal(spec.InputSchema, &schemaObject)
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("urn:probe-query", schemaObject); err != nil {
		return err
	}
	compiled, err := compiler.Compile("urn:probe-query")
	if err != nil {
		return err
	}
	if err = compiled.Validate(arguments); err != nil {
		return errors.New("model query violates real tool schema")
	}
	var actual f.Query
	if json.Unmarshal([]byte(call.Function.Arguments), &actual) != nil {
		return errors.New("invalid model query")
	}
	gotRaw, _ := json.Marshal(actual)
	if string(gotRaw) != string(expected) {
		return errors.New("model changed the requested query; not executed")
	}
	// Validate the real tool Schema, then execute the readonly backend.
	// No Runtime or persistence is involved.
	page, err := db.AgentData().Query(ctx, f.Scope{OwnerID: owner}, actual)
	if err != nil {
		return err
	}
	if len(page.Rows) > 2 {
		return errors.New("row limit exceeded")
	}
	for _, row := range page.Rows {
		if row["connection_id"] != connection || row["offering_id"] != offering {
			return errors.New("filter isolation failed")
		}
	}
	if page.HasMore && (page.NextOffset == nil || *page.NextOffset != len(page.Rows)) {
		return errors.New("invalid next offset")
	}
	result := f.ToolResult{OK: true, Data: f.CleanQueryData(page)}
	rawResult, _ := json.Marshal(result)
	request.Messages = append(request.Messages, first.Message, f.Message{Role: "tool", ToolCallID: call.ID, Content: string(rawResult)})
	request.Tools = nil
	if err = save("request-2.private.json", request); err != nil {
		return err
	}
	second, err := provider.Complete(ctx, request)
	_ = save("response-2.private.json", map[string]any{"response": second, "error": f.SafeError(err)})
	if err != nil {
		return err
	}
	if len(second.Message.ToolCalls) != 0 {
		return errors.New("model attempted another tool call")
	}
	summary := map[string]any{"model_requests": 2, "data_queries": 1, "returned_rows": len(page.Rows), "has_more": page.HasMore, "next_offset": page.NextOffset, "filters_verified": true, "readonly": true, "business_writes": 0, "note": "Only one requested tool call and a final response; no survey-completeness requirement."}
	if err = save("summary.json", summary); err != nil {
		return err
	}
	fmt.Println("single_tool_probe_passed", dir)
	return nil
}
