-- An ownerless customer record is nobody's to change until somebody claims
-- it. The rows written before creates stamped their creator — hand-made
-- records whose create carried no owner, leads a connector captured — would
-- otherwise all need a claim before their own maker could touch them again,
-- and a connector could not resume the leads it minted. Each row's
-- provenance already names that person: captured_by is `human:<uuid>` for a
-- hand-made row and `connector:<name>:<uuid>` for a captured one, so the
-- owner is the human the provenance ends in, where that human still exists.
-- A row whose provenance names nobody (system, a bare connector) stays
-- unowned and is claimed the ordinary way.
SET LOCAL lock_timeout = '3s';

UPDATE person p SET owner_id = u.id
  FROM app_user u
 WHERE p.owner_id IS NULL
   AND p.captured_by ~ '[0-9a-f-]{36}$'
   AND u.id = substring(p.captured_by from '([0-9a-f-]{36})$')::uuid;

UPDATE organization o SET owner_id = u.id
  FROM app_user u
 WHERE o.owner_id IS NULL
   AND o.captured_by ~ '[0-9a-f-]{36}$'
   AND u.id = substring(o.captured_by from '([0-9a-f-]{36})$')::uuid;

UPDATE lead l SET owner_id = u.id
  FROM app_user u
 WHERE l.owner_id IS NULL
   AND l.captured_by ~ '[0-9a-f-]{36}$'
   AND u.id = substring(l.captured_by from '([0-9a-f-]{36})$')::uuid;

UPDATE deal d SET owner_id = u.id
  FROM app_user u
 WHERE d.owner_id IS NULL
   AND d.captured_by ~ '[0-9a-f-]{36}$'
   AND u.id = substring(d.captured_by from '([0-9a-f-]{36})$')::uuid;
