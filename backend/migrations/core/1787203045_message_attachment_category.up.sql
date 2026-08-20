-- A file that arrived on a messaging channel is not an email attachment.
--
-- The captured-file writer stamped this column with a hardcoded literal, and the
-- vocabulary admitted only mail, so every file arriving on a messaging channel
-- would have landed durably mislabeled — in the row, in the audit image, and in
-- every category filter and document-library query. Widening the CHECK is what
-- lets the writer tell the truth.
--
-- ADDITIVE: no existing row changes. An email attachment stays an email
-- attachment; this only makes a second honest answer available.
--
-- Re-added in the IMMEDIATE form, deliberately, and NOT as NOT VALID + VALIDATE.
-- That split is the usual way to keep a validation scan off a write-blocking
-- lock, and here it would buy nothing: dbmigrate runs a whole migration file in
-- ONE transaction, so the ACCESS EXCLUSIVE lock the DROP takes is held to commit
-- and the VALIDATE would run under it anyway. The scan itself is free of risk —
-- the widened set is strictly weaker than the one it replaces, so every existing
-- row satisfies it by construction.
--
-- The wait for that lock IS bounded, which is the part that matters: a strong
-- request queues behind any running transaction and everything arriving after it
-- queues behind the request, so an unbounded wait turns one idle-in-transaction
-- session into an installation-wide write stall.
SET LOCAL lock_timeout = '3s';

ALTER TABLE attachment DROP CONSTRAINT attachment_category_check;
ALTER TABLE attachment
  ADD CONSTRAINT attachment_category_check
  CHECK (category IN ('contract','offer','legal','email_attachment',
                      'message_attachment','other'));
