package dbbridge

// Collection rules outlive rebuildable academic offerings. Their identity is the
// owner, connection, source and exact native course reference; display fields are
// snapshots, never matching keys.
const schemaV14 = `
CREATE TABLE collection_policies (
 user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 version integer NOT NULL CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE collection_exclusions (
 user_id text NOT NULL REFERENCES collection_policies(user_id) ON DELETE CASCADE,
 connection_id text NOT NULL, source text NOT NULL CHECK(source IN ('chaoxing','mooc')),
 course_ref text NOT NULL CHECK(course_ref<>''), offering_id text NOT NULL CHECK(offering_id<>''),
 name text NOT NULL, native_class_id text NOT NULL, native_term_id text NOT NULL,
 PRIMARY KEY(user_id,connection_id,source,course_ref), UNIQUE(user_id,offering_id),
 FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE
);
`
