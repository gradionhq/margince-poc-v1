# ADR-0043 — Humans log in with email and password against a server-side session

**Status:** Active
**Decided:** 2026-06-25

## The decision

The server owns human login itself. There is no external identity service
and no social sign-in. A human posts an email address and a password to
`POST /auth/login`; the server mints a 32-byte random token, stores only its
SHA-256 hash, and returns the raw token in a cookie named `crm_session`
(`HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`). The session row carries
both an idle timeout and an absolute expiry, and an operator can revoke it,
which a stateless signed token could not offer. Agents authenticate on a
different path entirely: an OAuth 2.1 passport, never a session cookie.

## Why

Remote revocation and a device list are the requirements that decide the
mechanism. A self-contained signed token cannot be withdrawn before it
expires without a server-side deny list, which is a session table under
another name. Running auth in-process also keeps the product installable
on a customer's own hardware, with no external service to reach.

## What it binds in this repository

- `backend/migrations/core/0003_sessions_passports.up.sql` creates the
  `session` table (`token_hash`, `idle_expires_at`, `expires_at`,
  `revoked_at`, `last_seen_at`, `user_agent`, `ip`) and the `passport`
  table, which binds an agent credential to the human it acts for through
  `on_behalf_of` and a `scopes` array.
- `backend/internal/modules/identity/handlers.go` defines
  `SessionCookieName = "crm_session"` and sets the cookie in
  `setSessionCookie` / `clearSessionCookie` with exactly those attributes.
- `backend/api/crm.yaml` carries `/auth/login`, `/auth/logout`,
  `/auth/capabilities`, `/auth/forgot-password`, `/auth/reset-password`,
  `/auth/change-password` and `/me`.
- `GET /auth/capabilities` is unauthenticated and reports which login
  methods actually work, including `password` and `password_reset`, so the
  login screen never shows a flow the installation cannot complete.
- Repeated failure locks the account:
  `backend/internal/modules/identity/lockout.go` drives
  `app_user.failed_login_count` and `app_user.locked_until`.
- Password rotation and recovery live in
  `backend/internal/modules/identity/changepassword.go`,
  `reset.go` and `passwordlink.go`.
- The whole identity module is `backend/internal/modules/identity/`; the
  admission gate that every request passes is `backend/internal/platform/auth`.

## History

Adopted from the retired specification, decided 2026-06-25. Rewritten in
plain language 2026-08-19. Two later amendments already hold in the code:
the browser extension signs in as the human over OAuth 2.1 with PKCE and
holds no agent credential, and the first organization plus its first admin
are created at API boot from the configuration file rather than by a public
`POST /workspaces` endpoint, which no longer exists.

Two claims in the source have not shipped. Multi-factor authentication and
SAML/OIDC single sign-on are described there as delivered; the identity
module contains neither, and `GET /auth/capabilities` exists precisely so
the login UI does not advertise them. The source also says every
authentication event lands in `audit_log`. `audit_log` records only changes to
records, so a login — which changes none — goes to `system_log` instead;
`backend/internal/modules/identity/service.go` writes the `login` entry there.
