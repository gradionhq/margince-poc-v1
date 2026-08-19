# ADR-0061 — One installation serves one organization, bootstrapped from a configuration file

**Status:** Active — the singleton bootstrap, the removed public endpoint, and
the honest login surface are built. Single sign-on and multi-factor
authentication are **not built**; see below.
**Decided:** 2026-07-17

## The decision

An installation serves exactly one organization. Nobody selects, enters,
creates, or switches one: the running server resolves its own, never from a
hostname, subdomain, header, or login field. The organization and its first
administrator are created once at first boot from a configuration file. On boot
the server takes a database advisory lock and counts active organizations — zero
creates one, one binds to it, more than one refuses to start and names the
violated rule. No public route creates an organization, and the login screen
offers only what the installation can actually do.

## Why

The product does not target shared multi-tenant hosting, yet the shipped surface
said otherwise: an unauthenticated route let any visitor create a tenant, the
login screen defaulted to signup, and the contract advertised challenge states
for flows that did not exist. A tenant-creation route on a production install is
attack surface with no user, and a dead recovery link is a lie told to someone
already locked out.

The internal boundary stays: `workspace_id` still threads tenant isolation,
foreign keys, sessions, passports, approvals, audit rows, and the outbox. What
changed is who creates it and how the installation finds it.

## What it binds in this repository

- `backend/internal/modules/identity/installation.go` holds the boot machine.
  `BootstrapInstallation` takes `pg_advisory_xact_lock` so two api processes
  cannot race, then applies the zero/one/many rule. `InstallationWorkspace`
  resolves the singleton for a request and caches it.
- The bootstrap input is a function, called only on the branch that creates the
  organization — the administrator's password secret may be deleted afterwards,
  so reading it eagerly would fail on installations that followed this record.
  A bootstrap value is consumed once; a restart resets no password, role, or
  seed.
- `/workspaces` and its request schema are gone from `backend/api/crm.yaml`;
  only a comment remains recording that the route was superseded. Anonymous
  `GET /auth/capabilities` replaced it, telling the login screen which methods
  are live so the frontend renders only working affordances.
- `config/margince.example.yaml` is the template; `make dev` copies it on first
  run and then leaves the copy alone, so local edits survive a restart.
- The rule is enforced at boot, not by a schema constraint. The schema can still
  hold several organization rows on purpose, because the cross-tenant isolation
  tests prove isolation by inserting a second one directly.

## What is owed

**Single sign-on is not built.** The record specifies OpenID Connect with an
authorization code flow, proof key exchange, state, nonce, an exact redirect
URI, and full identity-token validation — separate from the two existing OAuth
uses, neither of which signs a human in. It also settles the trap: the first
successful login must not claim the organization. Bootstrap creates a pending
administrator from the configured address; the first provider response whose
**verified** address matches activates that user and stores a permanent binding
to the provider's subject identifier, and later logins resolve by subject. The
details beyond that rule are not final.

**Multi-factor authentication is not built.** Its request field and two
challenge states were removed from the live contract rather than left
advertising a flow that does not exist; they return with the flows.

**One consequence of that gap is live.** On an installation with no outbound
email, an administrator may issue a set-password link for any member, including
one who already has a password. The named mitigation for high-blast-radius
administrator actions is step-up re-authentication, which needs the missing
second factor. Until then the controls are detective — an audit event naming
actor and target, plus a rate limit — and after the fact: redeeming the link
revokes every session the target holds.

**The healthy half of the recovery flag is not built.** The login screen reports
self-service recovery as available whenever an email channel is configured. A
configured-but-broken relay reports available, sends nothing, and blocks the
administrator-issued link that would have been the fallback.

## History

Adopted from the retired specification, decided 2026-07-17. Rewritten in plain
language 2026-08-19.

The source records one amendment, dated 2026-08-05, folded in above. The
original text said an operator-run command-line reset covered installations
without email. It covered administrator lockout, not member provisioning: an
administrator in the web UI holds no database credentials, so every new member
needed a shell invocation by a different person while the roster already showed
them as active. The amendment added the administrator-issued set-password link,
and separated redeeming a held token from requesting one by email — possession
of the token is the authority.
