-- 0273: an operator-supplied password must be replaced before the account is used.
--
-- A configured bootstrap (A107/ADR-0061 §2) has the OPERATOR choose the first
-- admin's password. The human who ends up using that account never picked it,
-- and until now nothing ever required them to: the credential an operator typed
-- into a deployment file stayed valid for the life of the installation.
--
-- Set ONLY by a configured bootstrap. A claim (ADR-0105) has the human choose
-- their own credential during the claim itself, so forcing a change there would
-- ask someone to replace a password they set seconds earlier and that nobody
-- else has ever known. An admin-initiated reset is likewise excluded — it has
-- its own flow, and its subject sets the password themselves through a link.
--
-- Cleared by the change, so the flag answers exactly one question: is this
-- account still using a password somebody else chose.
--
-- DEFAULT false, so every existing row and every other creation path is
-- unaffected: this is additive, and an installation that already replaced its
-- bootstrap password by hand is not asked to do it again.
ALTER TABLE app_user
  ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

-- An agent seat has no password and no role by design (seed-and-fixtures §1.5):
-- it is an identity, not an authority. A flag demanding it rotate a credential
-- it does not have could never be cleared, so the constraint states that the
-- combination is not merely absent but impossible.
ALTER TABLE app_user
  ADD CONSTRAINT app_user_agent_never_forced
  CHECK (NOT (is_agent AND must_change_password));

-- The same dead end reached from the other side, and the reason this is a
-- constraint rather than a convention: a flagged account with NO password can
-- never leave the state. Changing the password requires proving the current
-- one, which a null hash cannot do, and the reset flow refuses a passwordless
-- account outright — so the row would be locked out of every route with no
-- exit. Every writer that raises the flag sets a hash in the same statement;
-- this says that they must.
ALTER TABLE app_user
  ADD CONSTRAINT app_user_forced_rotation_needs_a_password
  CHECK (NOT (must_change_password AND password_hash IS NULL));
