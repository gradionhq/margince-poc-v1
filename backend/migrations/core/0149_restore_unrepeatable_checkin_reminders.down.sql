-- No-op, for the same reason 0148's down is one: this migration restored rows
-- rather than changing any schema, and it kept no record of which ids it
-- touched. Re-archiving "everything that matches" would take back the tasks a
-- person has since assigned or acted on, which is the harm this migration
-- exists to undo.
SELECT 1;
