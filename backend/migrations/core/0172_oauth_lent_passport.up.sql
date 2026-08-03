-- Which passport a human lent to a connection was answerable only from the
-- audit row (oauth_lend.go's auditLend), so Settings could name a connection's
-- client but never the credential it was derived from — the one question a
-- human revoking things actually asks. These two columns carry that fact
-- forward: the consent stamps the code, the redemption copies it onto the
-- grant, and the Settings list reads it from there.
--
-- PROVENANCE ONLY. It is not, and must not become, an input to any decision.
-- The lend boundary ends at the authorization code's own five minutes: a
-- passport revoked after consent does NOT stop the code from being redeemed,
-- because a connection borrows the consenting HUMAN's authority while the lent
-- passport contributes its scopes and then stops being party to it
-- (oauth_lend.go §"WHERE THAT LOCK'S REACH ENDS";
-- TestALentPassportRevokedAfterConsentStillRedeems). A future revalidation
-- against this column would silently move that boundary — a decision about
-- what a lend means, taken in a WHERE clause.
--
-- Hence NULL is ordinary, not a defect: every grant issued before this
-- migration has no recorded lend, and the UI omits the provenance line rather
-- than inventing one.
ALTER TABLE oauth_authorization_code ADD COLUMN lent_passport_id uuid NULL;
ALTER TABLE oauth_grant ADD COLUMN lent_passport_id uuid NULL;

-- ON DELETE SET NULL, unlike passport.oauth_grant_id's RESTRICT one table over:
-- that constraint guards a LIVE credential's link to the consent authorizing
-- it, and orphaning it would leave authority standing without a record. This
-- one names a passport that is already finished being party to the connection,
-- so a deleted passport costs a display line and nothing else — and RESTRICT
-- here would let a spent template block an erasure it has no authority over.
ALTER TABLE oauth_authorization_code ADD CONSTRAINT oauth_code_lent_passport_fkey
  FOREIGN KEY (workspace_id, lent_passport_id)
  REFERENCES passport (workspace_id, id) ON DELETE SET NULL (lent_passport_id);
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_lent_passport_fkey
  FOREIGN KEY (workspace_id, lent_passport_id)
  REFERENCES passport (workspace_id, id) ON DELETE SET NULL (lent_passport_id);

-- Postgres indexes the referenced side of a foreign key, never the referencing
-- side, so without these two every passport delete sequentially scans both
-- tables to find the rows it must NULL. The same reason 0150 added
-- passport_oauth_grant_ix for its cascade. Deleting a passport is not rare —
-- app_user's ON DELETE CASCADE reaches it whenever an account is erased — and
-- both referencing tables only grow: oauth_grant with every consent, and
-- oauth_authorization_code with every spent code.
CREATE INDEX oauth_code_lent_passport_ix
  ON oauth_authorization_code (workspace_id, lent_passport_id);
CREATE INDEX oauth_grant_lent_passport_ix
  ON oauth_grant (workspace_id, lent_passport_id);
