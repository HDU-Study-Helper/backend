package dbbridge

// V15 freezes scheduling metadata only for newly sealed batches. Existing batches
// retain NULL scope, their original contents, and their manual activation state.
const schemaV15 = `ALTER TABLE control_batches ADD COLUMN batch_scope jsonb DEFAULT NULL;`
