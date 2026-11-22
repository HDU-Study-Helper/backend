//go:build ignore
package main
import("context";"encoding/json";"fmt";"os";"time";"github.com/jackc/pgx/v5")
func main(){ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel();cfg,e:=pgx.ParseConfig(os.Getenv("DATABASE_URL"));if e!=nil{panic("config")};cfg.RuntimeParams["default_transaction_read_only"]="on";c,e:=pgx.ConnectConfig(ctx,cfg);if e!=nil{panic("database")};defer c.Close(ctx);tx,e:=c.BeginTx(ctx,pgx.TxOptions{IsoLevel:pgx.RepeatableRead,AccessMode:pgx.ReadOnly});if e!=nil{panic("readonly tx")};defer tx.Rollback(ctx)
queries:=map[string]string{
"rounds":`SELECT row_to_json(t) FROM(SELECT r.id,r.status,r.started_at,count(b.id) batches,coalesce(sum(b.change_count),0) changes,jsonb_object_agg(coalesce(b.status,'none'),1) status_keys FROM control_rounds r LEFT JOIN control_batches b ON b.round_id=r.id GROUP BY r.id ORDER BY r.started_at DESC)t`,
"batches":`SELECT row_to_json(t) FROM(SELECT id,round_id,owner_id,source,status,stage,change_count,attempt,error_code,batch_scope FROM control_batches ORDER BY created_at,id)t`,
"records":`SELECT row_to_json(t) FROM(SELECT source,kind,count(*) count FROM platform_records GROUP BY source,kind ORDER BY source,kind)t`,
"workflows":`SELECT row_to_json(t) FROM(SELECT workflow_id,owner_user_id,name,status,version,definition->'entity' entity FROM agent_workflows ORDER BY created_at)t`,
"connections":`SELECT row_to_json(t) FROM(SELECT user_id,id,source,enabled,state FROM platform_connections ORDER BY user_id,source,id)t`,
"dws":`SELECT row_to_json(t) FROM(SELECT c.user_id,c.id,c.enabled,c.state,(SELECT count(*) FROM dws_messages m WHERE(m.user_id,m.connection_id)=(c.user_id,c.id)) messages,(SELECT count(*) FROM dws_groups g WHERE(g.user_id,g.connection_id)=(c.user_id,c.id)) groups FROM dws_connections c)t`,
"running":`SELECT row_to_json(t) FROM(SELECT run_id,owner_user_id,agent_id,status FROM agent_runs WHERE status='running')t`,
"migrations":`SELECT row_to_json(t) FROM(SELECT max(version) version FROM dbbridge_migrations)t`,
}
report:=map[string]any{};for name,q:=range queries{rows,e:=tx.Query(ctx,q);if e!=nil{panic("query "+name)};out:=[]json.RawMessage{};for rows.Next(){var raw []byte;if e=rows.Scan(&raw);e!=nil{panic("scan")};out=append(out,raw)};rows.Close();if rows.Err()!=nil{panic("rows")};report[name]=out};raw,_:=json.MarshalIndent(report,"","  ");if e=os.WriteFile("../.build/backend-flash-full-20261115/inventory.private.json",raw,0600);e!=nil{panic("save")};fmt.Println(string(raw))}
