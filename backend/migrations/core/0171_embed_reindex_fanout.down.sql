ALTER TABLE embed_store_binding
  DROP CONSTRAINT embed_store_binding_run_shape,
  DROP COLUMN reembedding_pending,
  DROP COLUMN reembedding_identity;
