-- 0161: the per-domain organization verdict — what a mail domain is allowed to
-- create.
--
-- Capture used to derive an organization from every counterparty domain that
-- was not consumer mail. A personal domain is neither: sebastian@herpertz.net
-- is a person whose own domain carries his name, and naming a company
-- "Herpertz" after it manufactures junk that no later evidence removes.
--
-- The ladder now defers instead. A domain nothing is yet known about produces
-- the person and a 'pending' row here; a triage site read answers what the
-- domain is; the answer creates the organization or refuses it, once, for every
-- message from that domain afterwards. Without this table the answer would have
-- to be re-derived per message and a refusal would never stick.
--
-- One row per (workspace, registrable domain) — the eSLD, so mail from
-- sub.acme.com and acme.com ask the same question and get the same answer.

CREATE TABLE organization_domain_disposition (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  domain       text NOT NULL CHECK (domain = lower(domain) AND domain <> ''),

  -- pending  — asked, not yet answered; no organization exists for it.
  -- company  — answered yes; organization_id names what was created.
  -- personal — a natural person's own domain: a person, never a company.
  -- provider — a mailbox or hosting vendor. The site belongs to a real company
  --            (live.fr is Microsoft's) which is emphatically NOT the sender's
  --            employer, so it must not become their organization either.
  -- no_site  — nothing reachable to judge, and the sender's name does not
  --            explain the domain. The organization was created on the old
  --            terms; the row records that the question was already asked.
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'company', 'personal', 'provider', 'no_site')),
  -- What answered: the triage read, the sender-name heuristic when no site
  -- could be read, or a human whose own act settled it.
  source text NULL CHECK (source IS NULL OR source IN ('site_read', 'heuristic', 'human')),
  -- One sentence a human can act on: WHY this domain was refused a company.
  -- Operational, not evidence — the dossier holds what was actually read.
  evidence text NULL,

  -- The human whose connection first surfaced the domain. They own whatever the
  -- verdict creates, exactly as they own an ensure's records today; a domain
  -- nobody is accountable for may not mint rows.
  owner_id uuid NULL,
  CONSTRAINT organization_domain_disposition_owner_fkey
    FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id) ON DELETE SET NULL,

  -- The dossier that answered, and the organization a 'company' verdict made.
  -- Both nullable and both SET NULL on delete: the verdict outlives its
  -- evidence, and an archived organization must not drag the answer with it.
  -- Both carry workspace_id in the reference (the composite-FK convention,
  -- 0019) so a cross-workspace target is rejected by the database rather than
  -- by whichever query happens to remember to check.
  site_read_id    uuid NULL,
  CONSTRAINT organization_domain_disposition_site_read_fkey
    FOREIGN KEY (workspace_id, site_read_id)
    REFERENCES site_read (workspace_id, id) ON DELETE SET NULL,
  organization_id uuid NULL,
  CONSTRAINT organization_domain_disposition_org_fkey
    FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE SET NULL,

  -- Bounded retries with backoff, the same shape and the same reasons as
  -- capture_auto_enrich_state (0122): a site that will not load must not be
  -- re-crawled on every message. NULL next_attempt_at drops the row out of the
  -- due index for good — the question is settled or has run out of attempts.
  attempts        int NOT NULL DEFAULT 0,
  last_attempt_at timestamptz NULL,
  next_attempt_at timestamptz NULL DEFAULT now(),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- A settled verdict names what settled it. Without this a 'company' row could
  -- claim an organization it never created, or a refusal could carry no reason.
  CONSTRAINT organization_domain_disposition_settled_shape CHECK (
    (status = 'pending' AND source IS NULL) OR
    (status = 'company' AND source IS NOT NULL AND organization_id IS NOT NULL) OR
    (status IN ('personal', 'provider', 'no_site') AND source IS NOT NULL)
  )
);

-- One live question per domain. This is what makes two senders on a new domain
-- one triage rather than two, and what a concurrent ensure locks against.
CREATE UNIQUE INDEX uq_organization_domain_disposition
  ON organization_domain_disposition (workspace_id, domain);

-- The sweep's due-scan: only rows still owed an answer carry a due date.
CREATE INDEX idx_organization_domain_disposition_due
  ON organization_domain_disposition (next_attempt_at)
  WHERE next_attempt_at IS NOT NULL;

ALTER TABLE organization_domain_disposition ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_domain_disposition FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_domain_disposition_tenant_isolation ON organization_domain_disposition
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
