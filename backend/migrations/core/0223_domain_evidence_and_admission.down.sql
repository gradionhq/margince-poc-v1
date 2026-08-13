-- Reverting drops every standing refusal and every human admission with it, so
-- a domain an admin deliberately blocked becomes askable again and one they
-- deliberately let in loses the sticky override. The dispositions themselves
-- survive; only the admission layer above them is lost.

DROP INDEX idx_domain_disposition_unevidenced;
DROP INDEX idx_domain_disposition_admission;

ALTER TABLE organization_domain_disposition
  DROP CONSTRAINT organization_domain_disposition_admission_shape,
  DROP COLUMN admission_at,
  DROP COLUMN admission_source,
  DROP COLUMN admission_reason,
  DROP COLUMN admission,
  DROP COLUMN pending_reason;
