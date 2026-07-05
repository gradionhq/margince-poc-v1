# 0004 — Auth operational defaults (session TTLs, Argon2id params, workspace resolution)

Status: accepted · 2026-07-03

ADR-0043 fixes the mechanism (opaque 32-byte token, SHA-256 stored,
`crm_session` HttpOnly/Secure/SameSite=Strict cookie, idle + absolute
expiry, remote revoke at lookup) but not the numbers. This build uses:

- **Idle TTL 24h**, rolling forward on activity, capped by an **absolute
  TTL of 30 days**. Both enforced in the lookup predicate, not by a
  background job.
- **Argon2id** at the OWASP interactive-login baseline (t=2, m=19 MiB,
  p=1, 16-byte salt, 32-byte key), serialized as a PHC string so
  parameters can be raised without invalidating stored hashes.
- **Workspace resolution**: production resolves the tenant from the
  `{slug}.api.gradion.com` host (crm.yaml servers); local development
  uses the `X-Workspace-Slug` header. Session lookup happens *inside* the
  tenant transaction — there is no RLS-bypassing auth role.
- **Bootstrap seeding**: `POST /workspaces` creates workspace + admin +
  the five system roles in one transaction; crm-core's per-workspace
  defaults (the default pipeline) are seeded through an edge-composed
  hook so crm-auth never imports crm-core.

All four are operational defaults to re-ratify, not spec-derived facts.
