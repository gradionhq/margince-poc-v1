DROP TABLE embed_store_binding;
TRUNCATE embedding;                                              -- mixed-width rows can't cast back
ALTER TABLE embedding ALTER COLUMN embedding TYPE vector(1024);
CREATE INDEX idx_embedding_hnsw ON embedding USING hnsw (embedding vector_cosine_ops);
