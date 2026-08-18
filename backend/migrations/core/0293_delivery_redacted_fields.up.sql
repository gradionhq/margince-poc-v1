-- 0293: the delivery row can say which of its fields an erasure emptied
-- (A167/ADR-0116 §4-5).
--
-- comms_outbound stores an outbound message's recipients, subject and body a
-- second time behind the activity that records it. When an erasure meets a
-- Handelsbrief it RESTRICTS the activity rather than destroying it, and the
-- same per-datum rule reaches the delivery: the addressing goes, the commercial
-- substance stays. Without this column "we removed the recipients" and "this
-- message never had any" are the same empty list, and only the first is a
-- statement the controller must be able to make to the subject.
--
-- NOT NULL DEFAULT '{}' because "no redactions" is a real state that must not
-- be spelled the same way as "unknown" — the same reason activity's column
-- (0288) is shaped this way. The row carries no restriction state of its own:
-- a delivery has no existence apart from the activity it reports on, and two
-- independent restriction states could disagree.
ALTER TABLE comms_outbound
  ADD COLUMN redacted_fields text[] NOT NULL DEFAULT '{}';
