-- 0144: bound the preference-center token's lifetime. 0048 shipped it with
-- none — no expiry column, one permanently-reused token per person pinned by
-- a partial unique index, and a revoked_at that no production code wrote. But
-- that token is the SOLE authorization decision on the anonymous
-- /v1/public/preferences/{token} edge: it reads the recipient's per-purpose
-- consent state, withdraws, and GRANTS, under a system principal that
-- short-circuits every RBAC gate downstream. So a single leaked copy of a
-- single newsletter — from a mail archive, a gateway or spam scanner, a
-- forwarded message, a shared inbox — was un-rotatable standing authority
-- over that recipient for the life of the installation.
--
-- The window is generous where its sibling consent_doi_token's 72h is short,
-- because the two credentials answer opposite questions: an unclicked
-- confirmation is a refusal, while an unsubscribe link must keep working long
-- after the message that carried it. The send path slides expires_at forward
-- on every message and rotates the token outright once it passes an absolute
-- age ceiling, so a recipient who still receives mail always holds a working
-- link, an archived copy stops working a bounded time after the LAST message
-- instead of never, and rotation is what finally makes revoked_at a column
-- production writes. Both lengths have ONE spelling, in
-- internal/modules/consent/preference.go.
ALTER TABLE preference_token ADD COLUMN expires_at timestamptz;

-- Existing tokens are dated from their own mint, not from this deploy: a
-- token minted long ago must not be handed a fresh window by the migration
-- that introduces windows. One already past it stops resolving here, and the
-- recipient's next message mints a live replacement.
UPDATE preference_token SET expires_at = created_at + interval '30 days'
 WHERE expires_at IS NULL;

ALTER TABLE preference_token ALTER COLUMN expires_at SET NOT NULL;
