package dbbridge

const actionSchemaSQL = `
CREATE TABLE public_actions (
 owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 id text NOT NULL, kind text NOT NULL CHECK(kind IN ('todo','notice','job')),
 target_key text, source_agent_id text NOT NULL, source_run_id text NOT NULL,
 status text NOT NULL, input jsonb NOT NULL CHECK(jsonb_typeof(input)='object' AND octet_length(input::text)<=65536),
 due_at timestamptz, result jsonb CHECK(octet_length(result::text)<=65536), result_ref text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(owner_id,id), UNIQUE(owner_id,kind,target_key),
 CHECK((kind='todo' AND target_key IS NOT NULL AND status IN ('open','complete')) OR
 (kind='notice' AND target_key IS NULL AND status='recorded') OR
 (kind='job' AND target_key IS NULL AND due_at IS NOT NULL AND status IN ('pending','running','succeeded','failed','cancelled')))
);
CREATE INDEX public_actions_list ON public_actions(owner_id,kind,updated_at DESC,id);
CREATE INDEX public_actions_due ON public_actions(due_at,id) WHERE kind='job' AND status='pending';
CREATE TABLE reminder_keys(owner_id text NOT NULL,reminder_key text NOT NULL,action_id text NOT NULL,original_due timestamptz,original_mode text NOT NULL,repeat_weekly boolean NOT NULL,timezone text NOT NULL,preference_rev timestamptz, PRIMARY KEY(owner_id,reminder_key),FOREIGN KEY(owner_id,action_id) REFERENCES public_actions(owner_id,id) ON DELETE CASCADE);
CREATE TABLE action_receipts(owner_id text NOT NULL,kind text NOT NULL,request_key text NOT NULL,
 action_id text NOT NULL, request jsonb NOT NULL, PRIMARY KEY(owner_id,kind,request_key),
 FOREIGN KEY(owner_id,action_id) REFERENCES public_actions(owner_id,id) ON DELETE CASCADE);
`
