package dbbridge

// V9 copies every legacy memory conservatively as BASE, L1, agent PRI. IDs,
// content, category, priority and timestamps are preserved; legacy rows remain
// as an archive, and are no longer used by the application.
const schemaV9 = `
CREATE TABLE memories (
 memory_id text PRIMARY KEY,
 owner_user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 visibility text NOT NULL CHECK (visibility IN ('PUB','PRI')),
 owner_agent_id text,
 owner_workflow_id text,
 memory_type text NOT NULL CHECK (memory_type IN ('BASE','TEMP')),
 is_personal boolean NOT NULL DEFAULT false,
 level text NOT NULL CHECK (level IN ('L0','L1','L2','L3')),
 parent_memory_id text,
 title text NOT NULL CHECK (length(trim(title))>0 AND octet_length(title)<=2048),
 content text NOT NULL CHECK (length(trim(content))>0 AND octet_length(content)<=262144),
 structured_content jsonb CHECK (structured_content IS NULL OR jsonb_typeof(structured_content)='object'),
 source_refs jsonb CHECK (source_refs IS NULL OR jsonb_typeof(source_refs)='array'),
 valid_from timestamptz, valid_until timestamptz,
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived','invalid')),
 category text NOT NULL DEFAULT '', priority integer NOT NULL DEFAULT 0 CHECK(priority BETWEEN -100 AND 100),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_user_id,memory_id),
 CHECK ((visibility='PUB' AND owner_agent_id IS NULL AND owner_workflow_id IS NULL) OR
        (visibility='PRI' AND ((owner_agent_id IS NOT NULL AND owner_workflow_id IS NULL) OR
                              (owner_agent_id IS NULL AND owner_workflow_id IS NOT NULL)))),
 CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until>valid_from),
 CHECK (parent_memory_id IS NULL OR parent_memory_id<>memory_id),
 FOREIGN KEY(owner_user_id,owner_agent_id) REFERENCES agent_instances(owner_user_id,agent_id) ON DELETE CASCADE,
 FOREIGN KEY(owner_user_id,owner_workflow_id) REFERENCES agent_workflows(owner_user_id,workflow_id) ON DELETE CASCADE,
 FOREIGN KEY(owner_user_id,parent_memory_id) REFERENCES memories(owner_user_id,memory_id) ON DELETE SET NULL (parent_memory_id)
);
CREATE INDEX memories_scope ON memories(owner_user_id,visibility,priority DESC,created_at,memory_id);
CREATE INDEX memories_agent ON memories(owner_user_id,owner_agent_id) WHERE owner_agent_id IS NOT NULL;
CREATE INDEX memories_workflow ON memories(owner_user_id,owner_workflow_id) WHERE owner_workflow_id IS NOT NULL;
CREATE INDEX memories_level_type ON memories(owner_user_id,level,memory_type);
CREATE INDEX memories_validity ON memories(owner_user_id,status,valid_until,valid_from);
CREATE INDEX memories_parent ON memories(owner_user_id,parent_memory_id);
INSERT INTO memories(memory_id,owner_user_id,visibility,owner_agent_id,memory_type,level,title,content,category,priority,created_at,updated_at)
 SELECT memory_id,owner_user_id,'PRI',agent_id,'BASE','L1',title,content,category,priority,created_at,updated_at FROM agent_memories;

-- Parent organization is distinct from semantic relations. Levels strictly
-- increase down parent edges; parents must remain visible in the child's scope.
CREATE FUNCTION memory_parent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.owner_user_id,730022409));
 IF TG_OP='UPDATE' AND NEW.owner_user_id<>OLD.owner_user_id THEN
  RAISE EXCEPTION 'immutable memory tenant' USING ERRCODE='23514';
 END IF;
 IF NEW.parent_memory_id IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM memories p WHERE p.owner_user_id=NEW.owner_user_id AND p.memory_id=NEW.parent_memory_id
   AND p.level<NEW.level AND (p.visibility='PUB' OR
    (NEW.visibility='PRI' AND ((p.owner_agent_id IS NOT NULL AND p.owner_agent_id=NEW.owner_agent_id) OR
                              (p.owner_workflow_id IS NOT NULL AND p.owner_workflow_id=NEW.owner_workflow_id))))
 ) THEN RAISE EXCEPTION 'invalid memory parent' USING ERRCODE='23514'; END IF;
 IF EXISTS (
  SELECT 1 FROM memories c
  WHERE c.owner_user_id=NEW.owner_user_id AND c.parent_memory_id=NEW.memory_id
    AND NOT COALESCE(
      NEW.level<c.level AND (
        NEW.visibility='PUB' OR (
          c.visibility='PRI' AND (
            (NEW.owner_agent_id IS NOT NULL AND NEW.owner_agent_id=c.owner_agent_id) OR
            (NEW.owner_workflow_id IS NOT NULL AND NEW.owner_workflow_id=c.owner_workflow_id)
          )
        )
      ), false
    )
 )
 THEN RAISE EXCEPTION 'invalid memory children' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER memories_parent_guard BEFORE INSERT OR UPDATE ON memories FOR EACH ROW EXECUTE FUNCTION memory_parent_guard();

CREATE TABLE memory_relations (
 relation_id text PRIMARY KEY, owner_user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 source_memory_id text NOT NULL, target_memory_id text NOT NULL,
 relation_type text NOT NULL CHECK(relation_type IN ('RELATED_TO','ALIAS_OF','BELONGS_TO','DERIVED_FROM','ROUTES_TO','SUPERSEDES')),
 description text NOT NULL DEFAULT '' CHECK(octet_length(description)<=16384),
 created_by_agent_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_user_id,source_memory_id,target_memory_id,relation_type),
 CHECK(source_memory_id<>target_memory_id),
 FOREIGN KEY(owner_user_id,source_memory_id) REFERENCES memories(owner_user_id,memory_id) ON DELETE CASCADE,
 FOREIGN KEY(owner_user_id,target_memory_id) REFERENCES memories(owner_user_id,memory_id) ON DELETE CASCADE,
 FOREIGN KEY(owner_user_id,created_by_agent_id) REFERENCES agent_instances(owner_user_id,agent_id) ON DELETE CASCADE
);
CREATE INDEX memory_relations_target ON memory_relations(owner_user_id,target_memory_id,relation_type);
`
