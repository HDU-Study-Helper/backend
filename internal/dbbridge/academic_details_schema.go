package dbbridge

// V13 adds readable course content without changing the already-applied V12.
const schemaV13 = `
ALTER TABLE academic_offerings ADD COLUMN description text NOT NULL DEFAULT '';
ALTER TABLE academic_offerings ADD COLUMN details jsonb NOT NULL DEFAULT '{}' CHECK(octet_length(details::text)<=65536);
`
