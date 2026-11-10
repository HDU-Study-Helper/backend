package dbbridge

// Add HDU as a source without modifying already applied migrations.
const schemaV7 = `
ALTER TABLE platform_connections DROP CONSTRAINT platform_connections_source_check;
ALTER TABLE platform_connections ADD CONSTRAINT platform_connections_source_check CHECK(source IN ('chaoxing','mooc','ucampus','jwxt'));
`
