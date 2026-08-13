-- 0223: a company needs EVIDENCE, and a domain can be refused one outright.
--
-- Two problems, one ledger.
--
-- (1) When a site could not be read, ResolveUnreadableDomainTriage fell back to
-- the sender's own name and — when that name did not explain the domain —
-- created the company ANYWAY, named after the domain label. In a real import
-- that produced 40 of 108 organizations: "Pwc", "Mckinsey", "Saigonai",
-- "Ausgezeichnet", each with every enrichment field NULL, each frozen that way
-- because the disposition settled and nothing asks a settled domain again. An
-- unevidenced title-cased domain is worse than no record: it looks like a
-- company the workspace knows something about.
--
-- (2) Nothing could say "this domain never becomes a company". Vendors the
-- business merely USES — Expensify, TripIt, Xero, a ticket shop — have real
-- corporate websites, so every evidence test says yes. The refusal has to be a
-- decision about the DOMAIN, held independently of who wrote from it, or a
-- named employee writing from that domain re-opens the question and the company
-- appears after all.
--
-- The open/settled contract is untouched: `pending` still means open, and
-- Settled() still means anything else. A withheld domain stays PENDING, which
-- is what keeps it retryable and visible — a new status value would have been
-- read as settled by every due-scan, queue-mark and exhaustion query, and would
-- have needed all of them changed to mean what pending already means.

ALTER TABLE organization_domain_disposition
  -- Why a still-pending domain has no company yet. NULL is the ordinary case:
  -- the question is simply young. 'unevidenced' is the domain whose site could
  -- not be read and whose sender name explained nothing — open, awaiting either
  -- a readable site or a human, and NEVER auto-created on the strength of
  -- having been asked twice.
  ADD COLUMN pending_reason text NULL
    CHECK (pending_reason IS NULL OR pending_reason IN ('unevidenced')),

  -- A standing refusal, independent of any sender's kind. 'suppressed' is the
  -- vendor/service domain that must not become a company even when a real
  -- employee writes from it. 'admitted' is a human's override, and it is
  -- STICKY: no later model verdict or heuristic may re-suppress a domain a
  -- person deliberately let in.
  ADD COLUMN admission text NULL
    CHECK (admission IS NULL OR admission IN ('suppressed', 'admitted')),
  -- One sentence an admin reads in the blocked-domain list: why it was refused.
  ADD COLUMN admission_reason text NULL,
  -- What refused it, so a human decision is distinguishable from a machine's.
  ADD COLUMN admission_source text NULL
    CHECK (admission_source IS NULL OR admission_source IN ('verdict', 'heuristic', 'human')),
  ADD COLUMN admission_at timestamptz NULL,

  -- 'unevidenced' describes a question still WAITING, so it may only sit on a
  -- row that is still pending and not refused. Without this the partial index
  -- below would keep serving rows whose real state moved on — a settled company
  -- or a suppressed vendor listed to a human as "awaiting evidence".
  ADD CONSTRAINT organization_domain_disposition_pending_reason_shape CHECK (
    pending_reason IS NULL
    OR (status = 'pending' AND admission IS DISTINCT FROM 'suppressed')
  ),

  -- An admission is a claim about all three columns or none of them.
  ADD CONSTRAINT organization_domain_disposition_admission_shape CHECK (
    (admission IS NULL AND admission_reason IS NULL AND admission_source IS NULL
      AND admission_at IS NULL)
    OR
    (admission IS NOT NULL AND admission_reason IS NOT NULL
      AND admission_source IS NOT NULL AND admission_at IS NOT NULL)
  );

-- The admin list reads suppressed domains first and by recency; the withheld
-- review queue reads the unevidenced ones. Both are small, bounded scans that
-- would otherwise walk every domain the workspace has ever seen.
CREATE INDEX idx_domain_disposition_admission
  ON organization_domain_disposition (workspace_id, admission_at DESC)
  WHERE admission IS NOT NULL;

CREATE INDEX idx_domain_disposition_unevidenced
  ON organization_domain_disposition (workspace_id, updated_at DESC)
  WHERE pending_reason = 'unevidenced';
