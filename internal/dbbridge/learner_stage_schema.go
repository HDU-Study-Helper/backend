package dbbridge

const schemaV16 = `
CREATE TABLE learner_stage_commits (
 owner_user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 session_id text NOT NULL,
 request_hash text NOT NULL,
 receipt jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(owner_user_id,session_id)
);
`
