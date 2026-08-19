# ADR-0113 — An installation serves on a license and stops at the seats it grants

**Status:** Active — the boot refusal, the seat ceiling and the single count
all ship and are covered by tests.
**Decided:** 2026-08-15

## The decision

A production installation boots on a license or it does not boot. The deployment
posture decides which it is, and it fails closed: an installation that names no
posture is production and needs a license, while one that says it is
development, staging or test keeps running unlicensed. The refusal names both
ways out, because an operator seeing it is either licensed and missing the token
after a redeploy or running a development installation that never said so. The
granted seat count is a ceiling enforced where a seat comes into use: with every
licensed full seat taken, inviting a member is refused, and so is reactivating a
deactivated full seat. Nothing already in use is taken away — no seat is
demoted, no session ends, and an installation over its grant keeps every person
working. The ceiling is read live, so a license renewed in place raises it
without a restart.

## Why

Before this the entitlement was verified, shown on a screen, published as a
metric, and then ignored, so no moment ever required a license. The only
consequence for exceeding a grant was out of band: the release service
withholding updates weeks later, at an operator who did not make the decision,
in a form that names no seat. Refusing the next seat instead puts the refusal in
front of the admin making the decision, at the moment they make it.

## What it binds in this repository

- `backend/internal/platform/licensecheck/licensecheck.go` verifies the token
  offline, running the bundled WebAssembly module under `wazero` in this
  process. The module carries the public keyset it trusts, so an air-gapped
  installation proves its entitlement as a connected one does. The issuer,
  product name and grant generation are compiled-in constants, not settings. The
  module under `licensecheck/module/` is bundled as published and held to the
  digest beside it, so a swapped one fails the build.
- `backend/internal/platform/deployconfig/license.go` resolves the token: the
  `MARGINCE_LICENSE` environment variable, otherwise a file reference. A
  configured file that cannot be read, is empty, or is too large is an error,
  never a silent fall back to unlicensed.
- `backend/internal/compose/license.go` refuses an unlicensed production boot in
  `refuseUnlicensedProduction`, on both serving roles.
  `backend/internal/shared/runtimeenv` parses the posture and is fail-closed:
  anything that is not development, staging or test is production.
- `backend/internal/modules/identity/seatceiling.go` enforces the ceiling inside
  the writer's transaction, before the write it guards. Every seat creation
  serializes on one advisory lock: the ceiling is a property of the whole set of
  seats, so no finer key would be correct.
- `apperrors.ErrSeatLimitReached` maps to `403 seat_limit_reached` in
  `backend/internal/platform/httperr/httperr.go`, and the message carries both
  counts and both remedies: free a seat, or license more.
- `backend/internal/modules/identity/seatusage.go` holds the one count the
  entitlement screen, the `margince_license_seats` metric and the refusal all
  run: full seats whose status is neither suspended nor deactivated. It names
  the statuses that do not count, so a status added later counts by default.
- Covered by `identity/seatceiling_integration_test.go` and, over HTTP, by
  `compose/integration/seatceiling_http_integration_test.go`.

## Open questions this record does not settle

Whether an agent seat counts against the grant is not resolved. The shipped
count includes agents, because excluding them would let an installation act
without limit through agents. The license text in `LICENSE` says the opposite:
AI agents and service accounts are not seats. Both cannot be true of the same
number, and the wording is a question for counsel. On a fresh installation the
practical effect is one seat: the admin plus the agent runner.

The success path is not proven by a test here: the bundled module trusts only
the production keyset, so no token this repository can mint is accepted against
it, and acceptance is exercised only against a non-production authority.

## History

Adopted from the retired specification, decided 2026-08-15. Rewritten in plain
language 2026-08-19.

This amends the earlier licensing decision whose enforcement knew only the
release service, written before an installation could verify itself offline. The
gated set is unchanged and security fixes stay unconditional. It supersedes the
older "warned but never enforced in product" reading, for seat creation only. An
installation that was running unlicensed in production stops booting after an
upgrade, which is a breaking operational change.
