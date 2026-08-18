-- The account clock reads through indexes instead of scanning the timeline
-- (PO-DDL-4, migration 1787032690).
--
-- last_activity_of_organization asked its question as
-- `oid IN (l.organization_id, d.organization_id, r.organization_id)` over
-- activity_link LEFT JOINed to deal and relationship. That reads well and is
-- unsargable: the account id is compared against three columns of a joined
-- row, so no index on any of them can be used and every call seq-scans the
-- whole activity_link table.
--
-- The cost lands on the WRITE path, because the clock is maintained by
-- per-row triggers. Every employment written, ended or archived recomputes
-- its account's clock; so does every activity_link that reaches an account.
-- One call is one full timeline scan, so N writes cost N scans. Seeding
-- 5 000 employments against a 20 000-row timeline did not finish in
-- 9m43s of measured runtime; the same statement takes 3.4s once the arms
-- below can seek.
--
-- The three arms are exactly the three the prose of 1787032690 names, and
-- exactly the three activities.OrgReachSet walks: filed against the account,
-- against one of its deals, against a contact it currently employs. Written
-- apart, each one leads with the account id against a single indexed column
-- (idx_alink_org, idx_deal_org, the employment relationship), so the planner
-- seeks instead of scanning. max() over the union of the arms is the same
-- value the one-scan form computed — a duplicate row cannot change a maximum,
-- which is why the arms need no DISTINCT.
CREATE OR REPLACE FUNCTION last_activity_of_organization(oid uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(v) FROM (
    -- Filed against the account itself.
    SELECT max(a.occurred_at) AS v
      FROM activity_link l
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE l.organization_id = oid
    UNION ALL
    -- Filed against one of its deals.
    SELECT max(a.occurred_at)
      FROM deal d
      JOIN activity_link l ON l.deal_id = d.id
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE d.organization_id = oid
    UNION ALL
    -- Filed against a contact it currently employs.
    SELECT max(a.occurred_at)
      FROM relationship r
      JOIN activity_link l ON l.person_id = r.person_id
      JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
     WHERE r.organization_id = oid AND r.kind = 'employment'
       AND r.ended_at IS NULL AND r.archived_at IS NULL
  ) arms
$$;

-- The employment arm seeks on the account id alone. idx_rel_org_people leads
-- with workspace_id (ADR-0091 is retiring that column), so it cannot serve
-- this lookup; without an index here the arm trades one scan of the timeline
-- for one scan of the relationship table.
CREATE INDEX IF NOT EXISTS idx_rel_employer_people
  ON relationship (organization_id, person_id)
  WHERE kind = 'employment' AND ended_at IS NULL AND archived_at IS NULL;
