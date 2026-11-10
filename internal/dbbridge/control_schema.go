package dbbridge

const schemaV10 = `
CREATE TABLE control_rounds(id text PRIMARY KEY,status text NOT NULL,started_at timestamptz NOT NULL DEFAULT now(),lease_owner text NOT NULL DEFAULT '',heartbeat timestamptz NOT NULL DEFAULT now(),finished_at timestamptz,results jsonb NOT NULL DEFAULT '[]');
CREATE TABLE control_events(id text PRIMARY KEY,round_id text NOT NULL REFERENCES control_rounds(id),owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,source text NOT NULL,payload jsonb NOT NULL,batch_id text,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX control_events_unbatched ON control_events(round_id,owner_id,source) WHERE batch_id IS NULL;
CREATE TABLE control_batches(id text PRIMARY KEY,round_id text NOT NULL REFERENCES control_rounds(id),owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,source text NOT NULL,status text NOT NULL DEFAULT 'pending',stage text NOT NULL DEFAULT 'trigger',changes jsonb NOT NULL,change_count integer NOT NULL,attempt integer NOT NULL DEFAULT 0,trigger_output jsonb NOT NULL DEFAULT '{}',filter_output jsonb NOT NULL DEFAULT '{}',error_code text NOT NULL DEFAULT '',lease_owner text NOT NULL DEFAULT '',heartbeat timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX control_batches_queue ON control_batches(status,created_at);
CREATE TABLE control_runs(run_id text PRIMARY KEY,lease_owner text NOT NULL DEFAULT '',batch_id text NOT NULL DEFAULT '',owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,agent_id text NOT NULL,workflow_id text NOT NULL DEFAULT '',state text NOT NULL,record jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX control_runs_recent ON control_runs(updated_at DESC);
CREATE INDEX control_runs_batch ON control_runs(batch_id);
UPDATE agent_instances SET config=jsonb_set(config,'{limits,max_concurrent}',to_jsonb(GREATEST(COALESCE((config#>>'{limits,max_concurrent}')::int,1),CASE WHEN config->>'agent_type'='filter' THEN 4 ELSE 16 END))),updated_at=now() WHERE config->>'agent_type' IN ('filter','worker');
`
