-- Deliberately a no-op. The up migration archived reminder tasks; it did not
-- change the schema, and it left no record of which rows it touched. An
-- un-archive here could only guess from the same subject prefixes, and would
-- resurrect tasks a human archived on purpose along with the ones the
-- migration archived. Reverting to 0147 restores the schema, not the work
-- queue: archival is a data judgement, not reversible structure.
SELECT 1;
