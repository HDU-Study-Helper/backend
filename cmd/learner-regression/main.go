// Command learner-regression surveys archived courses and optionally runs Learner
// against a retained isolated snapshot. It never collects upstream data.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/dbbridge"
	stage "academic/backend/internal/learnerstage"
	"github.com/jackc/pgx/v5"
)

type options struct {
	execute, allowActiveSnapshot, prepareOnly             bool
	sourceSchema, owner, round, output, prompt, resumeDir string
	offering, closureStage, healthRequest                 string
	parallel, diagnosticTimeout                           int
	timeout                                               time.Duration
}
type course struct {
	Owner      string `json:"owner_id"`
	Connection string `json:"connection_id"`
	Source     string `json:"source"`
	ID         string `json:"offering_id"`
	Ref        string `json:"course_ref"`
	Name       string `json:"course_name"`
	Record     string `json:"record_id"`
}
type manifest struct {
	At           time.Time        `json:"snapshot_at"`
	Mode         string           `json:"mode"`
	SourceSchema string           `json:"source_schema"`
	TargetSchema string           `json:"target_schema,omitempty"`
	Round        string           `json:"round_id,omitempty"`
	Courses      []course         `json:"courses"`
	Counts       map[string]int64 `json:"source_counts"`
	Copies       map[string]int64 `json:"copied_rows,omitempty"`
	PromptSHA    string           `json:"learner_prompt_sha256"`
	Note         string           `json:"scope_note"`
	SourceActive int              `json:"source_activity_records"`
	CourseDigest string           `json:"course_manifest_sha256,omitempty"`
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Foreign-key order; no old Workflow, memory, run, session or queue state is copied.
var snapshotTables = []string{"users", "agent_instances", "agent_framework_settings", "platform_connections", "dws_connections", "dws_groups", "platform_records", "courses", "academic_offerings", "academic_items", "academic_item_evidence", "academic_item_personal_states", "schedules", "dws_messages", "dws_attachments"}

func main() {
	var o options
	flag.BoolVar(&o.execute, "execute", false, "create or explicitly resume an isolated regression and call real Learner models")
	flag.BoolVar(&o.prepareOnly, "prepare-only", false, "clone an isolated snapshot without calling models; requires --execute")
	flag.StringVar(&o.offering, "offering", "", "select one exact archived offering ID")
	flag.StringVar(&o.closureStage, "closure-stage", "", "isolated tools, model, learner, full, followup, revise or repair stage; requires --execute")
	flag.StringVar(&o.healthRequest, "health-request", "", "with model stage, load a retained request as context; never execute its tools")
	flag.IntVar(&o.parallel, "parallel", 2, "concurrent course cases (1–4), also bounded by original Agent and provider limits")
	flag.IntVar(&o.diagnosticTimeout, "diagnostic-model-timeout", 90, "closure model request timeout in seconds; isolated settings only")
	flag.StringVar(&o.resumeDir, "resume-dir", "", "retained regression directory to resume; requires --execute and never reads source")
	flag.BoolVar(&o.allowActiveSnapshot, "allow-active-snapshot", false, "freeze committed business data even when source has active status records; never copy or alter its execution state")
	flag.StringVar(&o.sourceSchema, "source-schema", "public", "explicit source schema, always read only")
	flag.StringVar(&o.owner, "owner", "", "optional owner ID; otherwise include all active owners")
	flag.StringVar(&o.round, "round", "", "optional stopped round; select its explicitly associated offerings")
	flag.StringVar(&o.output, "output-root", "../.build", "private output parent")
	flag.StringVar(&o.prompt, "prompt", "../agent/learner-agent/system-prompt.md", "Learner prompt to use only in the isolated schema")
	flag.DurationVar(&o.timeout, "course-timeout", 30*time.Minute, "per-course cancellation deadline")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, o options) error {
	if o.parallel < 1 || o.parallel > 4 {
		return errors.New("parallel must be between 1 and 4")
	}
	if (o.prepareOnly || o.closureStage != "") && !o.execute {
		return errors.New("prepare-only and closure-stage require explicit --execute")
	}
	if o.closureStage != "" && o.closureStage != "tools" && o.closureStage != "model" && o.closureStage != "learner" && o.closureStage != "full" && o.closureStage != "followup" && o.closureStage != "revise" && o.closureStage != "repair" && o.closureStage != "format" {
		return errors.New("unknown closure stage")
	}
	if o.resumeDir != "" {
		if o.closureStage != "" {
			return runRetainedClosure(ctx, o)
		}
		return resume(ctx, o)
	}
	if !identifier.MatchString(o.sourceSchema) || strings.HasPrefix(o.sourceSchema, "learner_regression_") || o.timeout <= 0 {
		return errors.New("invalid source schema or timeout")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil || os.Getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL required and must be valid; load environment without printing it")
	}
	cfg.RuntimeParams["search_path"] = pgx.Identifier{o.sourceSchema}.Sanitize()
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	source, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("source database unavailable")
	}
	defer source.Close(context.Background())
	tx, err := source.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("cannot begin read-only snapshot")
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='120s'"); err != nil {
		return errors.New("cannot bound source reads")
	}
	var active int
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM control_batches WHERE status IN ('running','queued'))+(SELECT count(*) FROM control_rounds WHERE status='refreshing')+(SELECT count(*) FROM platform_sync_status WHERE in_progress)+(SELECT count(*) FROM agent_runs WHERE status='running')`).Scan(&active)
	if err != nil {
		return errors.New("cannot inspect source activity")
	}
	if active != 0 && !o.allowActiveSnapshot {
		return errors.New("source has active status records; use --allow-active-snapshot for an isolated snapshot of committed business data, without changing source work")
	}
	if o.round != "" {
		var n int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM control_rounds WHERE id=$1 AND status<>'refreshing'`, o.round).Scan(&n)
		if err != nil || n != 1 {
			return errors.New("selected stopped round not found")
		}
	}
	courses, err := selectCourses(ctx, tx, o)
	if err != nil {
		return err
	}
	if len(courses) == 0 {
		return errors.New("no eligible archived course offerings; nothing executed")
	}
	prompt, err := os.ReadFile(o.prompt)
	if err != nil || len(prompt) == 0 {
		return errors.New("cannot read Learner prompt")
	}
	digest := sha256.Sum256(prompt)
	dir, err := os.MkdirTemp(o.output, "learner-regression-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return errors.New("cannot create private output directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	release, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	defer release()
	m := manifest{At: time.Now().UTC(), Mode: "dry-run", SourceSchema: o.sourceSchema, Round: o.round, Courses: courses, SourceActive: active, Counts: map[string]int64{}, PromptSHA: hex.EncodeToString(digest[:]), Note: "All active chaoxing/mooc structured offerings, or explicit selected-round associations; no 150-batch overview limit. Unassociated records remain in the copied business data but are not invented as course cases. No live platform collection. Source is READ ONLY. Target schema is retained, including on failure."}
	for _, table := range snapshotTables {
		var n int64
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
			return fmt.Errorf("cannot count source table %s", table)
		}
		m.Counts[table] = n
	}
	if err = save(filepath.Join(dir, "manifest.json"), m); err != nil {
		return err
	}
	fmt.Printf("Frozen %d course cases; private manifest: %s\n", len(courses), filepath.Join(dir, "manifest.json"))
	if !o.execute {
		return tx.Commit(ctx)
	}
	vault, err := loadVault()
	if err != nil {
		return err
	}
	schema, err := newSchema()
	if err != nil {
		return err
	}
	m.TargetSchema = schema
	m.Mode = "preparing"
	if err = save(filepath.Join(dir, "manifest.json"), m); err != nil {
		return err
	}
	// Only this connection can write; its search path contains no production fallback.
	targetCfg := cfg.Copy()
	targetCfg.RuntimeParams["default_transaction_read_only"] = "off"
	targetCfg.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	target, err := pgx.ConnectConfig(ctx, targetCfg)
	if err != nil {
		return errors.New("cannot open isolated target connection")
	}
	defer target.Close(context.Background())
	if _, err = target.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return errors.New("cannot create isolated schema; source unchanged")
	}
	targetDSN, err := isolatedDSN(os.Getenv("DATABASE_URL"), schema)
	if err != nil {
		return err
	}
	db, err := dbbridge.Open(ctx, targetDSN)
	if err != nil {
		return errors.New("cannot open isolated repository")
	}
	defer db.Close()
	var actualSchema string
	if err = target.QueryRow(ctx, "SELECT current_schema()").Scan(&actualSchema); err != nil || actualSchema != schema {
		return errors.New("isolated target schema verification failed")
	}
	if err = db.Migrate(ctx); err != nil {
		return errors.New("isolated migration failed; target schema retained")
	}
	copied, err := copySnapshot(ctx, tx, target, dir)
	if err != nil {
		return err
	}
	m.Copies = copied
	if err = tx.Commit(ctx); err != nil {
		return errors.New("cannot finish source snapshot")
	}
	m.CourseDigest = courseDigest(m.Courses)
	m.Mode = "executing"
	if err = save(filepath.Join(dir, "manifest.json"), m); err != nil {
		return err
	}
	if err = save(filepath.Join(dir, "courses.frozen.private.json"), m.Courses); err != nil {
		return err
	}
	if o.prepareOnly {
		m.Mode = "completed"
		m.Note += " Snapshot preparation only; no model calls or source collection."
		if err = save(filepath.Join(dir, "manifest.json"), m); err != nil {
			return err
		}
		fmt.Println("Prepared isolated snapshot:", dir)
		return nil
	}
	if o.closureStage != "" {
		err = executeClosure(ctx, o, db, target, vault, dir, m)
	} else {
		err = executeCourses(ctx, o, db, target, vault, dir, m, string(prompt), nil)
	}
	if ctx.Err() != nil {
		m.Mode = "interrupted"
	} else {
		m.Mode = "completed"
	}
	if saveErr := save(filepath.Join(dir, "manifest.json"), m); saveErr != nil {
		return saveErr
	}
	return err
}

func selectCourses(ctx context.Context, tx pgx.Tx, o options) ([]course, error) {
	rows, err := tx.Query(ctx, `SELECT a.user_id,a.connection_id,a.source,a.id,a.course_ref,a.name,a.record_id::text FROM academic_offerings a JOIN users u ON u.id=a.user_id JOIN platform_connections c ON(c.user_id,c.id)=(a.user_id,a.connection_id) WHERE NOT u.disabled AND c.enabled AND c.state='active' AND a.source IN('chaoxing','mooc') AND ($1='' OR a.user_id=$1) AND ($2='' OR EXISTS(SELECT 1 FROM control_batches b WHERE b.round_id=$2 AND b.owner_id=a.user_id AND b.source=a.source AND b.batch_scope->>'connection_id'=a.connection_id AND (b.batch_scope->>'offering_id'=a.id OR b.batch_scope->>'course_ref'=a.course_ref))) AND ($3='' OR a.id=$3) ORDER BY a.user_id,a.source,a.connection_id,a.id`, o.owner, o.round, o.offering)
	if err != nil {
		return nil, errors.New("cannot enumerate all eligible offerings")
	}
	defer rows.Close()
	out := []course{}
	for rows.Next() {
		var c course
		if err = rows.Scan(&c.Owner, &c.Connection, &c.Source, &c.ID, &c.Ref, &c.Name, &c.Record); err != nil {
			return nil, errors.New("cannot decode offering")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnConfig.ConnString preserves its original parsed text, so build a new DSN
// explicitly rather than relying on mutations to RuntimeParams being serialized.
func isolatedDSN(raw, schema string) (string, error) {
	if !isolatedSchemaPattern.MatchString(schema) {
		return "", errors.New("invalid isolated schema")
	}
	var dsn string
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("invalid database configuration")
		}
		q := u.Query()
		q.Set("search_path", schema)
		q.Set("default_transaction_read_only", "off")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		// Duplicate keyword keys use the final value in pgx's DSN parser.
		dsn = raw + " search_path='" + schema + "' default_transaction_read_only='off'"
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.RuntimeParams["search_path"] != schema || cfg.RuntimeParams["default_transaction_read_only"] != "off" {
		return "", errors.New("isolated database configuration verification failed")
	}
	return dsn, nil
}
func newSchema() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "learner_regression_" + time.Now().UTC().Format("20060102_150405") + "_" + hex.EncodeToString(b[:]), nil
}
func save(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, raw)
}
func copySnapshot(ctx context.Context, source pgx.Tx, target *pgx.Conn, dir string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range snapshotTables {
		rows, err := source.Query(ctx, "SELECT * FROM "+pgx.Identifier{table}.Sanitize())
		if err != nil {
			return nil, fmt.Errorf("cannot read snapshot table %s", table)
		}
		names := []string{}
		for _, fd := range rows.FieldDescriptions() {
			names = append(names, fd.Name)
		}
		values := [][]any{}
		for rows.Next() {
			v, e := rows.Values()
			if e != nil {
				rows.Close()
				return nil, fmt.Errorf("cannot decode snapshot table %s", table)
			}
			values = append(values, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot finish snapshot table %s", table)
		}
		// Keep an encrypted-settings/config backup, never decoded provider secrets.
		if table == "agent_framework_settings" || table == "agent_instances" {
			if err = save(filepath.Join(dir, table+".backup.private.json"), map[string]any{"columns": names, "rows": values}); err != nil {
				return nil, err
			}
		}
		n, err := target.CopyFrom(ctx, pgx.Identifier{table}, names, pgx.CopyFromRows(values))
		if err != nil {
			return nil, fmt.Errorf("cannot copy table %s; isolated schema retained", table)
		}
		if n != int64(len(values)) {
			return nil, fmt.Errorf("snapshot count mismatch for %s", table)
		}
		out[table] = n
	}
	// All copied source IDs are retained. Future target-only inserts must not reuse them.
	for _, table := range []string{"platform_records", "dws_messages"} {
		if _, err := target.Exec(ctx, "SELECT setval(pg_get_serial_sequence($1,'id'),GREATEST(COALESCE((SELECT max(id) FROM "+pgx.Identifier{table}.Sanitize()+"),0),1),true)", table); err != nil {
			return nil, fmt.Errorf("cannot advance isolated sequence for %s", table)
		}
	}
	return out, nil
}
func findLearner(ctx context.Context, db *dbbridge.DB, owner string) (f.AgentInstance, error) {
	var found f.AgentInstance
	after := ""
	for {
		page, err := db.Agents().ListAgents(ctx, owner, 100, after)
		if err != nil {
			return found, err
		}
		for _, a := range page {
			if a.Type == "learner" && a.Status == "enabled" {
				if found.ID != "" {
					return found, errors.New("multiple enabled Learners for owner")
				}
				found = a
			}
		}
		if len(page) < 100 {
			break
		}
		after = page[len(page)-1].ID
	}
	if found.ID == "" {
		return found, errors.New("enabled Learner missing")
	}
	return found, nil
}
func synchronizeLearners(ctx context.Context, db *dbbridge.DB, courses []course, prompt string) error {
	done := map[string]bool{}
	for _, c := range courses {
		if done[c.Owner] {
			continue
		}
		a, err := findLearner(ctx, db, c.Owner)
		if err != nil {
			return err
		}
		a.SystemPrompt = prompt
		a.EnabledTools = filter(a.EnabledTools, map[string]bool{"data_list_tables": true, "data_describe_table": true, "data_query": true, "data_search": true, "workflow_list": true, "workflow_get": true, "workflow_create": true, "workflow_update": true, "memory_search": true, "memory_list": true, "memory_get": true, "memory_get_relations": true, "memory_create": true, "memory_update": true, "memory_create_relation": true})
		a.Permissions = filter(a.Permissions, map[string]bool{"data.read": true, "workflow.read": true, "workflow.write": true, "memory.read": true, "memory.write": true})
		if _, err = db.Agents().PutAgent(ctx, a); err != nil {
			return err
		}
		done[c.Owner] = true
	}
	return nil
}
func filter(values []string, allowed map[string]bool) []string {
	out := []string{}
	for _, v := range values {
		if allowed[v] {
			out = append(out, v)
		}
	}
	return out
}
func caseInput(c course) map[string]any {
	return map[string]any{"task": "根据已归档授权数据，为此课程初始化可持续使用的Workflow。先核实课程身份，选择或暂存新建目标，按当前提示词分层调查并用Workflow与Memory工具编辑暂存定义和有来源的知识；记录追踪范围、用户要求及真实未知，不以已知ID断言所有记录已关联。不要使用initial_private_memories旧入口，不创建日程、不发送通知、不访问或修改外部平台。完成后读取learner_stage_state，返回匹配当前会话和修订号的READY_TO_COMMIT；正式提交与兼容回执由宿主校验产生。", "context": map[string]any{"request_type": "CREATE", "verification_replay": true, "entity": map[string]string{"kind": "course", "id": "source=" + c.Source + "|connection=" + c.Connection + "|course_ref=" + c.Ref, "name": c.Name}, "source": c.Source, "connection_id": c.Connection, "course_ref": c.Ref, "offering_id": c.ID, "course_record_id": c.Record}}
}

type caseResult struct {
	OfferingID      string       `json:"offering_id"`
	RunID           string       `json:"run_id"`
	Verified        bool         `json:"verified"`
	ValidationError string       `json:"validation_error,omitempty"`
	Output          string       `json:"output"`
	Error           *f.Error     `json:"error,omitempty"`
	Record          *f.RunRecord `json:"record,omitempty"`
	AttemptID       string       `json:"attempt_id,omitempty"`
	Disposition     string       `json:"disposition,omitempty"`
}

func verifyCreation(ctx context.Context, db *dbbridge.DB, c course, output string, runErr error, record *f.RunRecord) (bool, string) {
	if runErr != nil {
		return false, "runtime failed"
	}
	if record == nil || record.Status != "succeeded" {
		return false, "persisted successful run missing"
	}
	created := false
	for _, tool := range record.ToolExecutions {
		if tool.Name == "workflow_create" && tool.Error == nil {
			created = true
		}
	}
	if !created {
		return false, "successful workflow_create audit missing"
	}
	var r struct {
		Status   string `json:"status"`
		ID       string `json:"workflow_id"`
		Version  int    `json:"workflow_version"`
		Verified bool   `json:"verified"`
		Receipt  struct {
			SessionID       string   `json:"session_id"`
			Revision        int      `json:"staging_revision"`
			Operation       string   `json:"operation"`
			Status          string   `json:"status"`
			WorkflowID      string   `json:"workflow_id"`
			WorkflowVersion int      `json:"workflow_version"`
			MemoryIDs       []string `json:"memory_ids"`
			Verified        bool     `json:"verified"`
		} `json:"commit_receipt"`
	}
	if json.Unmarshal([]byte(output), &r) != nil || r.Status != "APPLIED" || r.ID == "" || !r.Verified || r.Receipt.SessionID == "" || r.Receipt.Revision < 1 || r.Receipt.Operation != "CREATE" || r.Receipt.Status != "COMMITTED" || !r.Receipt.Verified || r.Receipt.WorkflowID != r.ID || r.Receipt.WorkflowVersion != r.Version {
		return false, "no verified APPLIED staging creation receipt"
	}
	staging, ok := db.Agents().(stage.Store)
	if !ok {
		return false, "staging receipt store unavailable"
	}
	ledger, err := staging.LearnerStageReceipt(ctx, c.Owner, r.Receipt.SessionID)
	if err != nil || ledger.SessionID != r.Receipt.SessionID || ledger.Revision != r.Receipt.Revision || ledger.Operation != "CREATE" || ledger.Status != "COMMITTED" || !ledger.Verified || ledger.WorkflowID != r.ID || ledger.WorkflowVersion != r.Version || len(ledger.MemoryIDs) == 0 {
		return false, "durable staging receipt mismatch"
	}
	w, err := db.Agents().GetWorkflow(ctx, c.Owner, r.ID)
	if err != nil || w.Status != "active" || w.Version != r.Version {
		return false, "persisted workflow state/version mismatch"
	}
	var d struct {
		Entity struct{ Kind, ID string } `json:"entity"`
	}
	if json.Unmarshal(w.Definition, &d) != nil || d.Entity.Kind != "course" || d.Entity.ID != "source="+c.Source+"|connection="+c.Connection+"|course_ref="+c.Ref {
		return false, "persisted entity mismatch"
	}

	memories, err := db.Agents().SearchMemory(ctx, f.Scope{OwnerID: c.Owner, AgentID: w.CreatedBy, WorkflowID: w.ID}, f.MemorySearch{WorkflowID: w.ID, Limit: 1000})
	if err != nil {
		return false, "cannot read persisted private profile"
	}
	if len(memories) == 0 {
		return false, "no sourced Workflow private knowledge persisted"
	}
	seen := map[string]bool{}
	for _, m := range memories {
		var refs []json.RawMessage
		if m.WorkflowID != w.ID || m.OwnerID != c.Owner || m.AgentID != "" || m.Visibility != "PRI" || m.Status != "active" || strings.TrimSpace(m.Content) == "" || json.Unmarshal(m.SourceRefs, &refs) != nil || len(refs) == 0 {
			return false, "invalid private knowledge ownership, state or sources"
		}
		seen[m.ID] = true
	}
	if len(ledger.MemoryIDs) != len(r.Receipt.MemoryIDs) {
		return false, "output receipt memory count differs from ledger"
	}
	for i, id := range ledger.MemoryIDs {
		if !seen[id] || r.Receipt.MemoryIDs[i] != id {
			return false, "committed private knowledge missing or receipt ID mismatch"
		}
	}
	return true, ""
}
