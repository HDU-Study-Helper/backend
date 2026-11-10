package dbbridge

const schemaV8 = `
CREATE TABLE schedules (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 academic_year integer NOT NULL CHECK(academic_year BETWEEN 2000 AND 2100),
 term integer NOT NULL CHECK(term IN (1,2)),
 teaching_week integer NOT NULL CHECK(teaching_week BETWEEN 1 AND 60),
 start_time timestamptz NOT NULL,
 end_time timestamptz NOT NULL CHECK(end_time>start_time),
 title text NOT NULL CHECK(length(title)>0), details text NOT NULL DEFAULT '',
 source text NOT NULL CHECK(source IN ('TIMETABLE','USER','AGENT')),
 event_status text NOT NULL DEFAULT 'INCOMPLETE' CHECK(event_status IN ('INCOMPLETE','COMPLETED')),
 mod jsonb NOT NULL DEFAULT '{"history":[]}' CHECK(jsonb_typeof(mod)='object'),
 series_key text NOT NULL DEFAULT '', origin_key text NOT NULL DEFAULT '',
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_trunc('minute',start_time)=start_time AND date_trunc('minute',end_time)=end_time),
 CHECK((source='TIMETABLE' AND series_key<>'' AND origin_key<>'') OR (source<>'TIMETABLE' AND series_key='' AND origin_key=''))
);
CREATE INDEX schedules_teaching_week ON schedules(user_id,academic_year,term,teaching_week,start_time,id) WHERE active;
CREATE INDEX schedules_time ON schedules(user_id,start_time,end_time) WHERE active;
CREATE UNIQUE INDEX schedules_origin ON schedules(user_id,academic_year,term,origin_key) WHERE source='TIMETABLE';
`
