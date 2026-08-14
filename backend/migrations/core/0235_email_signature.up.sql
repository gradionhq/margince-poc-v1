-- 0235: the sender's own sign-off, one per user.
--
-- Every mail this product sends goes out unsigned. The drafting prompts have
-- always told the model NOT to write a sign-off — "the composer adds the
-- sender's own; a name you guessed would go out over the wrong signature" —
-- and nothing ever added one, so the instruction described a step that did not
-- exist. This table is that step.
--
-- It is the SENDER's, not the message's: a signature is who you are, and
-- storing a copy per mail would leave every already-sent message carrying an
-- old address the day somebody changes theirs. The body keeps its own copy at
-- send time (comms_outbound snapshots what was transmitted), which is the
-- record of what actually went out.
--
-- No workspace_id and no row-level security, matching every table added since
-- ADR-0091: one installation serves one organization (A107), and there is no
-- second workspace for a policy to isolate this from. What protects it is the
-- store's own gate — a signature is readable and writable by its owner alone,
-- which is stricter than any RBAC object could express, because nobody else
-- has a reason to read the words another person signs their mail with.
CREATE TABLE email_signature (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  -- One per user. The unique constraint is the cardinality rule: a second
  -- signature would need a name and a chooser in the composer, and neither
  -- exists — multiple signatures per sender is a later question (Attio has
  -- them; Close deliberately does not).
  owner_id    uuid NOT NULL UNIQUE REFERENCES app_user (id) ON DELETE CASCADE,
  -- Plain text today. An HTML signature needs a multipart body to carry it and
  -- an editor to write it, neither of which the product has yet; a column that
  -- accepted markup before the transport could render it would put tags in the
  -- text/plain part of every mail.
  body        text NOT NULL,
  version     bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz NULL
);

CREATE TRIGGER trg_email_signature_updated BEFORE UPDATE ON email_signature
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
