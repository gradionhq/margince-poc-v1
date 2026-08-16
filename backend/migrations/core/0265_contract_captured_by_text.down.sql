-- Reverting to a uuid FK would refuse every prefixed principal already stored,
-- so the down migration only relaxes the NOT NULL it added.
ALTER TABLE contract ALTER COLUMN captured_by DROP NOT NULL;
