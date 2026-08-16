-- Reverse of 0272: nothing.
--
-- The up half changes no schema and no row — it reads, and either passes or
-- refuses. There is no state to put back, and a down that invented one would be
-- claiming this migration did something it did not.
--
-- Rolling back past this version is therefore free, which is the honest
-- property for a gate: it never becomes the reason an operator cannot go
-- backwards.
SELECT 1;
