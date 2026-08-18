-- Back to the one-scan form 1787032690 shipped.
DROP INDEX IF EXISTS idx_rel_employer_people;

CREATE OR REPLACE FUNCTION last_activity_of_organization(oid uuid) RETURNS timestamptz
LANGUAGE sql STABLE AS $$
  SELECT max(a.occurred_at)
    FROM activity_link l
    LEFT JOIN deal d ON d.id = l.deal_id
    LEFT JOIN relationship r ON r.person_id = l.person_id AND r.kind = 'employment'
      AND r.ended_at IS NULL AND r.archived_at IS NULL
    JOIN activity a ON a.id = l.activity_id AND a.archived_at IS NULL
   WHERE oid IN (l.organization_id, d.organization_id, r.organization_id)
$$;
