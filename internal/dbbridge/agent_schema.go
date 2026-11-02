package dbbridge

// schemaV5 is additive. Existing migrations and application tables are unchanged.
const schemaV5 = `
CREATE TABLE agent_instances (
 agent_id text PRIMARY KEY, owner_user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 config jsonb NOT NULL, status text NOT NULL CHECK(status IN ('enabled','disabled')),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_user_id,agent_id)
);
CREATE INDEX agent_instances_owner ON agent_instances(owner_user_id,agent_id);
CREATE TABLE agent_memories (
 memory_id text PRIMARY KEY, owner_user_id text NOT NULL, agent_id text NOT NULL,
 title text NOT NULL, content text NOT NULL CHECK(octet_length(content)<=262144), category text NOT NULL DEFAULT '',
 priority integer NOT NULL CHECK(priority BETWEEN -100 AND 100),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(owner_user_id,agent_id) REFERENCES agent_instances(owner_user_id,agent_id) ON DELETE CASCADE
);
CREATE INDEX agent_memories_load ON agent_memories(owner_user_id,agent_id,priority DESC,created_at,memory_id);
CREATE TABLE agent_workflows (
 workflow_id text PRIMARY KEY, owner_user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name text NOT NULL, description text NOT NULL DEFAULT '', parent_workflow_id text,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'), version integer NOT NULL CHECK(version>0),
 status text NOT NULL CHECK(status IN ('draft','active','archived')), created_by_agent_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_user_id,workflow_id), CHECK(parent_workflow_id IS NULL OR parent_workflow_id<>workflow_id),
 FOREIGN KEY(owner_user_id,created_by_agent_id) REFERENCES agent_instances(owner_user_id,agent_id),
 FOREIGN KEY(owner_user_id,parent_workflow_id) REFERENCES agent_workflows(owner_user_id,workflow_id)
);
CREATE INDEX agent_workflows_owner ON agent_workflows(owner_user_id,workflow_id);
CREATE TABLE agent_workflow_versions (
 owner_user_id text NOT NULL, workflow_id text NOT NULL, version integer NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(owner_user_id,workflow_id,version),
 FOREIGN KEY(owner_user_id,workflow_id) REFERENCES agent_workflows(owner_user_id,workflow_id) ON DELETE CASCADE
);
CREATE TABLE agent_runs (
 run_id text PRIMARY KEY, owner_user_id text NOT NULL, agent_id text NOT NULL, parent_run_id text,
 record jsonb NOT NULL, status text NOT NULL CHECK(status IN ('running','succeeded','failed')),
 started_at timestamptz NOT NULL, finished_at timestamptz,
 UNIQUE(owner_user_id,run_id),
 FOREIGN KEY(owner_user_id,agent_id) REFERENCES agent_instances(owner_user_id,agent_id) ON DELETE CASCADE,
 FOREIGN KEY(owner_user_id,parent_run_id) REFERENCES agent_runs(owner_user_id,run_id)
);
CREATE INDEX agent_runs_owner ON agent_runs(owner_user_id,agent_id,started_at DESC);
`
