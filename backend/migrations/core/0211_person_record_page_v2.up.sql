-- 0211 — the person record page V2 (ADR-0096/A147, ADR-0097/A148, ADR-0098/A149).
--
-- Five things the page needs and no table holds today: what was promised and
-- asked in captured conversations, the per-viewer brief cache, the dismissal
-- of a rendered moment, the consent CLASS that decides whether a purpose is
-- consent-gated at all, and a person's uploaded photo.
--
-- The three kinds of table here answer to different rules, and mixing them up
-- is how a read-model cache ends up carrying business truth:
--
--   conversation_claim is a BUSINESS ENTITY. It is written through the audited
--   write shape (domain row + audit_log + event_outbox in one transaction),
--   corrections are shared truth, and it feeds the page, the meeting brief and
--   the task edge.
--
--   person_brief is a READ-MODEL CACHE keyed per reader, exactly like org_brief
--   (migration 0143) and org_dossier (0199). No audit row, no outbox event; a
--   full rebuild is the remedy for any corruption.
--
--   person_moment_dismissal is PER-VIEWER VIEW STATE, ratified narrowly by
--   ADR-0096 Decision 3. It feeds no formula, no rule, no brief, and no other
--   viewer's page.

-- ---------------------------------------------------------------------------
-- 1. Conversation claims (ADR-0097 Decision 1)
-- ---------------------------------------------------------------------------

-- One store, one extraction mechanism, eight kinds. Commitments, open
-- questions, decisions and the what-matters rows share a lifecycle —
-- extracted → cited → correctable → dismissible — and differ only in kind, so
-- three tables would be three copies of the same correction machinery.
--
-- communication_preference is deliberately NOT a kind: observed-style
-- inference is dropped from the product (ADR-0097 Decision 1, founder ruling).
CREATE TABLE conversation_claim (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id       uuid NOT NULL,

  kind            text NOT NULL CHECK (kind IN (
                    'commitment_ours', 'commitment_theirs', 'open_question',
                    'decision', 'priority', 'objection', 'success_criterion',
                    'decision_process')),

  -- What the claim says, in the reader's language. Never the raw snippet:
  -- that is evidence, and it lives beside this in source_quote.
  body            text NOT NULL,

  -- GROUNDING. A claim without a source activity and the verbatim snippet it
  -- was read from is not storable — the extractor drops an ungrounded
  -- candidate rather than writing it (ADR-0097: grounded or silent). Both
  -- columns are NOT NULL so the invariant is the database's, not the caller's.
  source_activity_id uuid NOT NULL,
  source_quote       text NOT NULL,

  -- open → the loop is live; done → it was kept or answered; dismissed → a
  -- human said it was never a claim. Dismissed rows stay: they are what stops
  -- the next extraction run resurrecting the same wrong claim.
  status          text NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'done', 'dismissed')),
  due_at          timestamptz NULL,

  -- A human correction is typed-by-you and FINAL against the machine. A later
  -- run may add claims but never overwrites or resurrects a corrected one
  -- while the evidence it was corrected against is unchanged — which is what
  -- the fingerprint pins. Change the evidence and the correction re-arms.
  corrected_by_user_id uuid NULL,
  corrected_at         timestamptz NULL,
  evidence_fingerprint text NOT NULL,

  -- Contradictory evidence for the same claim renders "Needs review" with both
  -- sources. Newest-wins is not a resolution (ADR-0097 Decision 1).
  needs_review    boolean NOT NULL DEFAULT false,

  -- The task an extracted commitment_ours created, so completing either side
  -- completes both and dismissing the claim closes the task it created. A task
  -- IS an activity with kind='task' in this schema — there is no separate task
  -- table — so this references activity like the source does.
  task_activity_id uuid NULL,

  source          text NOT NULL,
  captured_by     text NOT NULL,

  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,

  -- Composite references carry workspace_id into the key. A single-column
  -- reference lets a row name a person or an activity in ANOTHER workspace:
  -- the target exists, so the database accepts it, and only application code
  -- remembering to filter stands between that row and a cross-tenant read.
  CONSTRAINT conversation_claim_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT conversation_claim_activity_fkey FOREIGN KEY (workspace_id, source_activity_id)
    REFERENCES activity (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT conversation_claim_task_fkey FOREIGN KEY (workspace_id, task_activity_id)
    REFERENCES activity (workspace_id, id) ON DELETE SET NULL,
  CONSTRAINT conversation_claim_corrector_fkey FOREIGN KEY (workspace_id, corrected_by_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL
);

-- The page's read: this person's live claims, newest first, filtered by kind.
CREATE INDEX conversation_claim_person_ix
  ON conversation_claim (workspace_id, person_id, kind)
  WHERE archived_at IS NULL;

-- The extractor's idempotence read: has this activity already been mined?
CREATE INDEX conversation_claim_activity_ix
  ON conversation_claim (workspace_id, source_activity_id)
  WHERE archived_at IS NULL;

CREATE TRIGGER trg_conversation_claim_updated BEFORE UPDATE ON conversation_claim
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

COMMENT ON TABLE conversation_claim IS
  'What was promised, asked and decided in captured conversations (ADR-0097 D1). A business entity, written through the audited write shape — not a cache.';
COMMENT ON COLUMN conversation_claim.evidence_fingerprint IS
  'Pins the evidence a correction was made against. Unchanged fingerprint → the correction holds; changed → it re-arms.';

-- ---------------------------------------------------------------------------
-- 2. The person brief cache (ADR-0097 Decision 4)
-- ---------------------------------------------------------------------------

-- The company brief pattern applied to a person, mirroring org_brief exactly:
-- per viewer, cached on an input fingerprint, cited output, generated_by
-- visible. The reader is part of the primary key for the reason org_dossier
-- spells out — row scope over CITED records cannot be summarized into a
-- signature, because a shared assembly would disclose that a record EXISTS to
-- a reader who may not see it.
CREATE TABLE person_brief (
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id       uuid NOT NULL,
  person_id     uuid NOT NULL,
  -- The assembled input plus the prompt and model-routing versions. A change
  -- in any of the three invalidates, so a prompt revision cannot serve
  -- yesterday's brief beside today's.
  fingerprint   text NOT NULL,
  payload       jsonb NOT NULL,
  generated_by  text NOT NULL CHECK (generated_by IN ('model', 'deterministic')),
  generated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id, person_id),
  CONSTRAINT person_brief_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT person_brief_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE
);

-- The primary key leads with user_id, so nothing supports the person FK's
-- cascade. Art. 17 erasure deletes people; without this index that cascade
-- sequentially scans the table once per deleted person, and at one row per
-- reader per person that is the whole table each time. Same reasoning as
-- migration 0143 for org_brief.
CREATE INDEX person_brief_person_ix ON person_brief (workspace_id, person_id);

COMMENT ON TABLE person_brief IS
  'Read-model cache (ADR-0097 D4), keyed per READER: no brief crosses readers, whatever their masks.';

-- ---------------------------------------------------------------------------
-- 3. Person moment dismissal — per-viewer view state (ADR-0096 Decision 3)
-- ---------------------------------------------------------------------------

-- Ratified narrowly and named explicitly in ADR-0096's sanctioned list. This
-- is presentation memory, not a business fact: it feeds no formula, no moment
-- rule, no brief, no agent context and no other viewer's page.
--
-- The dismissal is keyed to the EVIDENCE FINGERPRINT, not to the moment's
-- path. Keying on the path is what makes a dismissal survive the world
-- changing underneath it — the reader dismisses "she went quiet", a reply
-- arrives, and the page stays silent about the thing that just changed. The
-- fingerprint re-arms the moment when its evidence moves.
CREATE TABLE person_moment_dismissal (
  workspace_id         uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id              uuid NOT NULL,
  person_id            uuid NOT NULL,
  claim_key            text NOT NULL,
  evidence_fingerprint text NOT NULL,
  dismissed_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, user_id, person_id, claim_key),
  CONSTRAINT person_moment_dismissal_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT person_moment_dismissal_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX person_moment_dismissal_person_ix
  ON person_moment_dismissal (workspace_id, person_id);

COMMENT ON TABLE person_moment_dismissal IS
  'Per-viewer view state (ADR-0096 D3). Held against an evidence fingerprint so a dismissal re-arms when the evidence moves.';

-- ---------------------------------------------------------------------------
-- 4. Consent classes and qualifying events (ADR-0098)
-- ---------------------------------------------------------------------------

-- ADR-0011 made every outbound purpose default-deny. That overshoots on one
-- class: replying to a person who wrote to us. Individual business
-- correspondence is not advertising under UWG §7, and its lawful basis is
-- Art 6(1)(b)/(f) — not consent. Consent, with its German evidence standard,
-- belongs to marketing. The class column is what tells the gate which question
-- to ask.
ALTER TABLE consent_purpose ADD COLUMN class text NOT NULL DEFAULT 'marketing'
  CHECK (class IN ('business_correspondence', 'transactional', 'marketing', 'phone_outreach'));

COMMENT ON COLUMN consent_purpose.class IS
  'Which gate this purpose answers to (ADR-0098 D1). business_correspondence and transactional are never consent-gated; marketing needs DOI proof or the §7(3) flag.';

-- Defaulting to 'marketing' is the safe direction: a purpose whose class
-- nobody has decided keeps the strict gate it has today. The two non-consent
-- classes and the renamed phone purpose are then named explicitly. Every write
-- below binds app.workspace_id per workspace and carries its own workspace
-- predicate — FORCE row-level security binds the table owner, which is the
-- role migrations run as, and an UPDATE whose policy resolves to NULL reports
-- success having changed nothing.
-- business_correspondence is new: ADR-0011 never had a lane for "answering
-- someone who wrote to us", which is why answering was formally a violation.
-- Every existing workspace gains it here so the gate has something to resolve
-- the moment it starts asking about the class; the bootstrap seed plants it
-- for workspaces created after this migration.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    -- The conflict branch updates a row the VALUES clause never named, and an
    -- executor that bypasses row-level security would let that conflict land
    -- on another workspace's row. So the branch names its own target.
    INSERT INTO consent_purpose (workspace_id, key, label, requires_double_opt_in, class)
    VALUES (ws, 'business_correspondence', 'Business correspondence', false, 'business_correspondence')
    ON CONFLICT (workspace_id, key) DO UPDATE SET class = 'business_correspondence'
    WHERE consent_purpose.workspace_id = ws;

    UPDATE consent_purpose SET class = 'transactional'
    WHERE key = 'transactional'
      AND consent_purpose.workspace_id = ws;

    UPDATE consent_purpose SET class = 'phone_outreach'
    WHERE key IN ('phone_outreach', 'marketing_phone')
      AND consent_purpose.workspace_id = ws;
  END LOOP;
END $$;

-- The qualifying event that flipped business_correspondence to allowed, and
-- when. Deterministic and derivable from captured data — but RECORDED, because
-- Art 5(2) accountability is the difference between a defensible balancing
-- decision and an assertion. Which event flipped it is what a dispute asks for.
CREATE TABLE consent_qualifying_event (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id     uuid NOT NULL,
  -- inbound_message  — they wrote to us
  -- inquiry          — they initiated a form, booking or call
  -- active_deal      — an open deal or contract with their organization
  -- in_person        — a recorded exchange, typed by a named human
  kind          text NOT NULL CHECK (kind IN
                  ('inbound_message', 'inquiry', 'active_deal', 'in_person')),
  -- The record that proves it: an activity, a deal, or NULL for in_person,
  -- where the human's typed note IS the evidence.
  source_entity_type text NULL CHECK (source_entity_type IN ('activity', 'deal')),
  source_entity_id   uuid NULL,
  note          text NULL,
  occurred_at   timestamptz NOT NULL,
  source        text NOT NULL,
  captured_by   text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT consent_qualifying_event_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  -- in_person carries a typed note and no record; every other kind points at
  -- the record that proves it. A kind that proves nothing is not a qualifying
  -- event, so the database refuses the combination rather than the gate
  -- discovering it at send time.
  CONSTRAINT consent_qualifying_event_evidence CHECK (
    (kind = 'in_person' AND note IS NOT NULL)
    OR (kind <> 'in_person' AND source_entity_type IS NOT NULL AND source_entity_id IS NOT NULL)
  )
);

CREATE INDEX consent_qualifying_event_person_ix
  ON consent_qualifying_event (workspace_id, person_id, occurred_at DESC);

-- One event per source record, enforced by the database rather than by a
-- read-then-write check in Go. The derived arm stamps the event that authorized
-- a send, and two concurrent sends to the same person both see "no row yet" and
-- both insert: a NOT EXISTS guard in application code cannot close that race,
-- and the result is two rows claiming two events where one message happened.
--
-- Partial, because the in_person kind carries no source record: those rows are
-- typed by a named human, one deliberate act at a time, and nothing about them
-- is derivable to duplicate.
CREATE UNIQUE INDEX consent_qualifying_event_source_unique
  ON consent_qualifying_event (workspace_id, person_id, source_entity_type, source_entity_id)
  WHERE source_entity_id IS NOT NULL;

COMMENT ON TABLE consent_qualifying_event IS
  'The recorded event that flipped business correspondence to allowed (ADR-0098 D2). Recording it is what makes the Art 6(1)(f) balancing accountable.';

-- The UWG §7(3) existing-customer flag: the ONLY way marketing email is
-- lawful without consent, and it carries what a dispute demands. All four
-- cumulative conditions are columns, not prose — the address was obtained in
-- connection with a sale, the sends concern own similar goods, no objection
-- stands, and the opt-out notice was given at collection.
CREATE TABLE consent_existing_customer_flag (
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id          uuid NOT NULL,
  sale_reference     text NOT NULL,
  collected_at       timestamptz NOT NULL,
  similar_goods_note text NOT NULL,
  optout_notice_given boolean NOT NULL,
  set_by_user_id     uuid NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  -- A contact's employer change resets correspondence qualification and this
  -- flag: the old relationship's bases do not transfer to a new company.
  revoked_at         timestamptz NULL,
  revoked_reason     text NULL,
  PRIMARY KEY (workspace_id, person_id),
  CONSTRAINT consent_existing_customer_person_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT consent_existing_customer_setter_fkey FOREIGN KEY (workspace_id, set_by_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL,
  -- The flag without the notice is not the §7(3) flag; it is a claim to one.
  CONSTRAINT consent_existing_customer_notice CHECK (optout_notice_given)
);

COMMENT ON TABLE consent_existing_customer_flag IS
  'UWG §7(3) existing-customer flag with its four cumulative conditions as columns (ADR-0098 D4).';

-- The proof bundle a confirmation records. ADR-0011 already stores the wording
-- and its version verbatim (consent_event.policy_text / policy_version); what
-- was missing is what CAUSED the mail and what the confirming request looked
-- like. Without the trigger the chain ask → mail → click has a gap exactly
-- where a dispute asks its question.
ALTER TABLE consent_event ADD COLUMN issuance_trigger text NULL;
ALTER TABLE consent_event ADD COLUMN confirm_ip text NULL;
ALTER TABLE consent_event ADD COLUMN confirm_user_agent text NULL;

COMMENT ON COLUMN consent_event.issuance_trigger IS
  'What caused the confirmation mail to be sent (ADR-0098 D5), so the whole chain — ask → mail → click — is provable.';

-- ---------------------------------------------------------------------------
-- 5. The person photo (ADR-0096 Decision 5)
-- ---------------------------------------------------------------------------

-- Mirrors organization.logo_* exactly: a reference to normalized bytes in
-- object storage, NULL → the render layer shows the deterministic monogram.
-- The ONLY writer is a human upload. No connector address-book sync, no
-- Gravatar (an email hash sent to a third party on render is egress, P7), no
-- provider photos. A person's likeness enters the system only because a user
-- deliberately put it there.
ALTER TABLE person ADD COLUMN photo_object_key text NULL;
ALTER TABLE person ADD COLUMN photo_origin text NULL;

COMMENT ON COLUMN person.photo_origin IS
  'How the photo got here (ADR-0096 D5). Human upload is the only writer — never a connector, never Gravatar.';

-- ---------------------------------------------------------------------------
-- 6. Row-level security
-- ---------------------------------------------------------------------------

-- Every new tenant table takes FORCE row-level security with the deny-on-unset
-- semantics every other one carries: an unbound app.workspace_id resolves the
-- policy to NULL and sees nothing, rather than seeing everything.
ALTER TABLE conversation_claim ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_claim FORCE ROW LEVEL SECURITY;
CREATE POLICY conversation_claim_ws ON conversation_claim
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE person_brief ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_brief FORCE ROW LEVEL SECURITY;
CREATE POLICY person_brief_ws ON person_brief
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE person_moment_dismissal ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_moment_dismissal FORCE ROW LEVEL SECURITY;
CREATE POLICY person_moment_dismissal_ws ON person_moment_dismissal
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE consent_qualifying_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent_qualifying_event FORCE ROW LEVEL SECURITY;
CREATE POLICY consent_qualifying_event_ws ON consent_qualifying_event
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE consent_existing_customer_flag ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent_existing_customer_flag FORCE ROW LEVEL SECURITY;
CREATE POLICY consent_existing_customer_flag_ws ON consent_existing_customer_flag
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
