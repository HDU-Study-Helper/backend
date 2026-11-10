package dbbridge

// V12 is an entirely rebuildable projection. Raw platform records and credentials
// remain authoritative and are never rewritten by this migration.
const schemaV12 = `
ALTER TABLE platform_records ADD CONSTRAINT platform_records_owner_connection_id UNIQUE(user_id,connection_id,id);
CREATE TABLE courses (
 user_id text NOT NULL, connection_id text NOT NULL,id text NOT NULL,source text NOT NULL CHECK(source IN ('chaoxing','mooc')),
 native_course_id text NOT NULL,name text NOT NULL,
 PRIMARY KEY(user_id,connection_id,id),UNIQUE(user_id,connection_id,source,native_course_id),
 FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE
);
CREATE TABLE academic_offerings (
 user_id text NOT NULL,connection_id text NOT NULL,id text NOT NULL,course_id text NOT NULL,source text NOT NULL,
 course_ref text NOT NULL,native_course_id text NOT NULL,native_class_id text NOT NULL,native_term_id text NOT NULL,
 course_type text NOT NULL,name text NOT NULL,teacher text NOT NULL,school text NOT NULL,
 source_terms jsonb NOT NULL,coverage jsonb NOT NULL,record_id bigint NOT NULL,observed_at timestamptz NOT NULL,
 PRIMARY KEY(user_id,connection_id,id),UNIQUE(user_id,connection_id,course_ref),
 FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE,
 FOREIGN KEY(user_id,connection_id,course_id) REFERENCES courses(user_id,connection_id,id),
 FOREIGN KEY(user_id,connection_id,record_id) REFERENCES platform_records(user_id,connection_id,id) ON DELETE CASCADE
);
CREATE TABLE academic_items (
 user_id text NOT NULL,connection_id text NOT NULL,id text NOT NULL,source text NOT NULL,offering_id text,
 course_ref text NOT NULL,course_name text NOT NULL,kind text NOT NULL,task_type text NOT NULL,title text NOT NULL,body text NOT NULL,author text NOT NULL,
 native_id text NOT NULL,native_course_id text NOT NULL,native_class_id text NOT NULL,native_term_id text NOT NULL,source_term_id text NOT NULL,
 chapter_id text NOT NULL,lesson_id text NOT NULL,unit_id text NOT NULL,content_id text NOT NULL,path jsonb NOT NULL,
 published_at timestamptz,updated_at timestamptz,start_at timestamptz,end_at timestamptz,deadline_at timestamptz,
 published_time jsonb NOT NULL,updated_time jsonb NOT NULL,start_time jsonb NOT NULL,end_time jsonb NOT NULL,deadline_time jsonb NOT NULL,
 status text NOT NULL CHECK(status IN ('submitted','completed','open','needs_redo','unknown')),source_status text NOT NULL,
 metadata jsonb NOT NULL CHECK(octet_length(metadata::text)<=32768),missing jsonb NOT NULL,observed_at timestamptz NOT NULL,
 PRIMARY KEY(user_id,connection_id,id),
 FOREIGN KEY(user_id,connection_id) REFERENCES platform_connections(user_id,id) ON DELETE CASCADE,
 FOREIGN KEY(user_id,connection_id,offering_id) REFERENCES academic_offerings(user_id,connection_id,id) ON DELETE CASCADE
);
CREATE TABLE academic_item_evidence (
 user_id text NOT NULL,connection_id text NOT NULL,item_id text NOT NULL,record_id bigint NOT NULL,
 resource_kind text NOT NULL,external_id text NOT NULL,parent_id text NOT NULL,logical_id text NOT NULL,
 PRIMARY KEY(user_id,connection_id,item_id,record_id),
 FOREIGN KEY(user_id,connection_id,item_id) REFERENCES academic_items(user_id,connection_id,id) ON DELETE CASCADE,
 FOREIGN KEY(user_id,connection_id,record_id) REFERENCES platform_records(user_id,connection_id,id) ON DELETE CASCADE
);
CREATE TABLE academic_item_personal_states (
 user_id text NOT NULL,connection_id text NOT NULL,item_id text NOT NULL,state jsonb NOT NULL CHECK(octet_length(state::text)<=32768),
 PRIMARY KEY(user_id,connection_id,item_id),
 FOREIGN KEY(user_id,connection_id,item_id) REFERENCES academic_items(user_id,connection_id,id) ON DELETE CASCADE
);
CREATE INDEX academic_offerings_scope ON academic_offerings(user_id,connection_id,source,name,id);
CREATE INDEX academic_items_course_kind ON academic_items(user_id,connection_id,offering_id,kind,id);
CREATE INDEX academic_items_status ON academic_items(user_id,connection_id,status,id);
CREATE INDEX academic_items_publication ON academic_items(user_id,connection_id,published_at,id);
CREATE INDEX academic_items_deadline ON academic_items(user_id,connection_id,deadline_at,id);
CREATE INDEX academic_items_text ON academic_items USING gin(to_tsvector('simple',title||' '||body));
CREATE INDEX academic_item_evidence_record ON academic_item_evidence(user_id,connection_id,record_id);
`
