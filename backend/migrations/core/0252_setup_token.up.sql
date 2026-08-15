-- 0252: the setup token an unprovisioned installation is claimed with.
--
-- ADR-0105/A156 gives an installation two provisioning paths. Configured:
-- margince.yaml carries bootstrap_admin and boot creates the organization, as
-- A107/ADR-0061 §2 has always done. Claimed: it carries none, first boot mints
-- a single-use token, and the first human presents that token to create the
-- organization and their own credential.
--
-- The token is what keeps the claim route from being a public "become root"
-- endpoint. A reachable installation must not be claimable by whoever finds the
-- URL first (A107 §4 forbids exactly that for re-bootstrap), so the route is
-- inert without a secret only the operator can read out of the server log.
--
-- No workspace_id, and that is not an omission: the row exists BEFORE the
-- organization it authorizes creating. It is therefore outside the tenant
-- boundary and outside RLS, like workspace itself — there is no tenant to scope
-- it to, and adding a policy keyed on a GUC nothing has set would deny the one
-- caller that needs it.
--
-- Only the HASH is stored. The plaintext is written once to the server log and
-- to a file the operator reads; a database copy would outlive the claim it
-- authorizes and turn a backup into a bootstrap credential.
CREATE TABLE setup_token (
  id          uuid        PRIMARY KEY DEFAULT uuidv7(),
  token_hash  text        NOT NULL UNIQUE,
  created_at  timestamptz NOT NULL DEFAULT now(),
  consumed_at timestamptz
);

-- At most one token may be outstanding. A second unconsumed row would mean two
-- live claim credentials, and revoking one would not revoke the other — so the
-- re-mint path (the operator CLI, for a token lost before first use) must
-- consume or delete the old one rather than issue alongside it.
CREATE UNIQUE INDEX setup_token_one_outstanding
  ON setup_token ((consumed_at IS NULL)) WHERE consumed_at IS NULL;
