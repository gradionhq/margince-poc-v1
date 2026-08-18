-- last_activity_at on person and organization (PO-DDL-1/-4 as amended
-- 2026-08-18): when something last happened with the record, maintained on
-- the activity write exactly as deal.last_activity_at is — a read accelerator
-- the timeline can always rebuild, never a second truth. It backs the
-- "Last activity" sort on both lists (DM-VOCAB-1/2).
--
-- A person's clock is the newest occurred_at of an activity linked to them.
-- An account's clock is the newest occurred_at of an activity that REACHES
-- it — filed against it, against one of its deals, or against a contact it
-- currently employs — the same three arms activities.OrgReachSet walks for
-- the account timeline, so the column and the timeline cannot disagree.
--
-- The indexes are in the ORDER BY's own shape (see 0292): the sort column,
-- then the keyset tie-breakers, partial on the live rows, descending only —
-- recency is asked for newest-first.

ALTER TABLE person       ADD COLUMN IF NOT EXISTS last_activity_at timestamptz NULL;
ALTER TABLE organization ADD COLUMN IF NOT EXISTS last_activity_at timestamptz NULL;

UPDATE person p
   SET last_activity_at = t.newest
  FROM (SELECT l.person_id, max(a.occurred_at) AS newest
          FROM activity_link l
          JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
         WHERE l.person_id IS NOT NULL
         GROUP BY l.person_id) t
 WHERE p.id = t.person_id;

UPDATE organization o
   SET last_activity_at = t.newest
  FROM (SELECT reach.organization_id, max(a.occurred_at) AS newest
          FROM (SELECT l.activity_id, x.org_id AS organization_id
                  FROM activity_link l
                  LEFT JOIN deal d ON d.id = l.deal_id
                  LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
                    AND r.ended_at IS NULL AND r.archived_at IS NULL
                  CROSS JOIN LATERAL (VALUES (l.organization_id), (d.organization_id), (r.organization_id)) AS x(org_id)
                 WHERE x.org_id IS NOT NULL) reach
          JOIN activity a ON a.id = reach.activity_id AND a.archived_at IS NULL
         GROUP BY reach.organization_id) t
 WHERE o.id = t.organization_id;

CREATE INDEX IF NOT EXISTS idx_person_last_activity_keyset
  ON person (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_org_last_activity_keyset
  ON organization (last_activity_at DESC NULLS LAST, created_at DESC, id DESC)
  WHERE archived_at IS NULL;
