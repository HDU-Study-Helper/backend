package dbbridge

// V4 keeps the latest surviving observation (observed_at DESC, id DESC).
// V1–V3 deduplicated repeated hashes: a historical A/B/A return may already
// have discarded the final A. That missing observation cannot be reconstructed.
const schemaV4 = `
DELETE FROM platform_records p USING (
 SELECT id,row_number() OVER(PARTITION BY user_id,connection_id,kind,external_id,parent_id ORDER BY observed_at DESC,id DESC) AS rank
 FROM platform_records
) old WHERE p.id=old.id AND old.rank>1;

CREATE TEMP TABLE dbbridge_message_winners ON COMMIT DROP AS
 SELECT id AS old_id,first_value(id) OVER(PARTITION BY user_id,connection_id,group_id,external_id ORDER BY observed_at DESC,id DESC) AS winner_id
 FROM dws_messages;

-- Include attachments on every old version, even when the winning message had
-- no attachment. For collisions prefer the newest message that has that ID.
CREATE TEMP TABLE dbbridge_attachment_winners ON COMMIT DROP AS
 SELECT DISTINCT ON(a.user_id,a.connection_id,w.winner_id,a.id)
 a.user_id,a.connection_id,w.winner_id AS message_id,a.id,a.name,a.media_type,a.sha256,a.data
 FROM dws_attachments a
 JOIN dbbridge_message_winners w ON w.old_id=a.message_id
 JOIN dws_messages m ON m.id=a.message_id
 ORDER BY a.user_id,a.connection_id,w.winner_id,a.id,m.observed_at DESC,m.id DESC;
DELETE FROM dws_attachments;
INSERT INTO dws_attachments(user_id,connection_id,message_id,id,name,media_type,sha256,data)
 SELECT user_id,connection_id,message_id,id,name,media_type,sha256,data FROM dbbridge_attachment_winners;
DELETE FROM dws_messages m USING dbbridge_message_winners w WHERE m.id=w.old_id AND w.old_id<>w.winner_id;

-- PostgreSQL truncates generated constraint names. Find the old unique keys
-- by their column definitions rather than relying on those generated names.
DO $$ DECLARE c record; BEGIN
 FOR c IN
 SELECT conrelid::regclass AS rel,conname FROM pg_constraint
 WHERE contype='u' AND (
 (conrelid='platform_records'::regclass AND conkey=ARRAY[
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='user_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='connection_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='kind'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='external_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='parent_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='platform_records'::regclass AND attname='hash') ]::smallint[])
 OR (conrelid='dws_messages'::regclass AND conkey=ARRAY[
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='user_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='connection_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='group_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='external_id'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='source_version'),
 (SELECT attnum FROM pg_attribute WHERE attrelid='dws_messages'::regclass AND attname='content_hash') ]::smallint[]))
 LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.rel,c.conname); END LOOP;
END $$;
ALTER TABLE platform_records ADD CONSTRAINT platform_records_identity UNIQUE(user_id,connection_id,kind,external_id,parent_id);
ALTER TABLE dws_messages ADD CONSTRAINT dws_messages_identity UNIQUE(user_id,connection_id,group_id,external_id);
`
