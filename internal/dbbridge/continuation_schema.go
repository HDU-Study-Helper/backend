package dbbridge

const continuationSchemaSQL = `
CREATE TABLE agent_continuation_archive (
 owner_user_id text NOT NULL, run_id text NOT NULL, archive jsonb NOT NULL,
 PRIMARY KEY(owner_user_id,run_id),
 FOREIGN KEY(owner_user_id,run_id) REFERENCES agent_runs(owner_user_id,run_id) ON DELETE CASCADE
);
CREATE TABLE agent_continuation_questions (
 owner_user_id text NOT NULL, question_id text NOT NULL, channel text NOT NULL CHECK(channel IN ('ChA','ChB')), source_key text NOT NULL,
 run_id text NOT NULL, question jsonb NOT NULL, status text NOT NULL CHECK(status IN ('pending','running','completed','cancelled')) DEFAULT 'pending', answer_hash text, answer_text text, result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(owner_user_id,question_id), UNIQUE(owner_user_id,run_id),
 FOREIGN KEY(owner_user_id,run_id) REFERENCES agent_continuation_archive(owner_user_id,run_id) ON DELETE CASCADE
);
CREATE INDEX agent_pending_questions ON agent_continuation_questions(owner_user_id,channel,source_key,created_at) WHERE status='pending';
`
