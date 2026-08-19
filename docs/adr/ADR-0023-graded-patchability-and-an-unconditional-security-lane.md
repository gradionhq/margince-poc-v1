# ADR-0023 — Patchability is graded by how far a fork stays inside the seams, and security fixes ship to everyone

**Status:** Active — the signed release channel and the private reporting path
are built. The security fast lane is **not built** and is the part still owed;
see below.
**Decided:** 2026-06-17

## The decision

A customer may change this source, and how much of a patch guarantee they keep
depends on where they changed it. Three grades: a change that only adds files,
adds `x_`-prefixed columns, or lives in a fork-owned migration namespace keeps a
guaranteed mechanical patch; a change that edits a shared mapping or validation
function keeps the guarantee under supervision, applied by merging both sides
rather than picking one; a change that overrides a core invariant carries no
guarantee, and the modifier owns the result. Every release is one signed
artifact delivered over a channel that suits the deployment mode. Security fixes
are served unconditionally — no licence key, an expired key, a revoked key, an
over-seat install, and a terminated install all receive them.

## Why

Customers are expected to modify this source, so "we cannot patch you, you
changed it" has to be answered before an incident rather than during one. The
grades turn that answer into something a customer can check against their own
diff in advance. The unconditional security lane exists because withholding a
security fix over a billing dispute puts a third party's data at risk, and the
published licence commitment says security fixes reach every install.

## What it binds in this repository

- `.github/workflows/release.yml` cuts a release for every build that lands on
  `main`: it drafts the version, attaches the freshly generated software bills
  of materials, builds the three role images through `docker-bake.hcl`, and
  publishes only a complete draft.
- `make sbom` regenerates the three bills of materials, and the release
  workflow regenerates them rather than trusting the committed copies, which may
  lag the tree.
- The image build passes `--provenance "mode=max"`, so each published image
  carries build provenance a customer can verify.
- `SECURITY.md` is the reporting half: a weakness goes to a private GitHub
  Security Advisory, acknowledged within three business days and assessed within
  ten. It states plainly that a public report before a fix ships puts every
  deployment at risk.
- `backend/migrations/core/` and `backend/migrations/custom/` are the namespace
  split the grades are defined against: upstream writes the first, a fork writes
  the second, and `scripts/check-migration-versions.sh` (run by
  `make migration-versions`) catches a collision between them.

## What is owed

**The security fast lane.** The record says a security fix ships as a small
isolated change against core, cherry-pickable onto the current release and the
two before it plus the long-term line, without taking a feature upgrade. This
repository publishes whole releases only. There is no backport line, no
long-term support branch, and no patch-only mode. **Not built**, and this is the
half worth keeping: it matters the first time a deployed installation needs a
security fix it cannot take a feature upgrade to receive.

The cadence and severity targets in the source were starting anchors to
recalibrate on real use. They are not commitments this repository meets today.

## What was retired

**The three patch grades**, and the classifier that would have reported which
grade a fork falls into, were retired 2026-08-19 without being built. Grading a
fork's upgrade guarantee by how far it stays inside the seams is elaborate
machinery for a problem this product does not have yet: no customer has forked
in volume, and the grades are a promise nobody has asked to verify. The
namespace split the grades were defined against — `backend/migrations/core/`
against `backend/migrations/custom/`, and the fork-owned directories under
`modules/` — stands on its own and is the part that actually protects an
upgrade.

**Licence enforcement and the seat true-up** are not this repository's to
record. Commercial terms and the release-service entitlement check are decided
and held elsewhere, so a record here would be a second copy of somebody else's
decision, free to drift from it. What this repository keeps is the one rule that
binds the code: **no authentication or entitlement step may run before a
security fix is served.** An expired key must never block a security update.
That carve-out is load-bearing wherever the check eventually lives, and it is
stated here because the code that would implement it is here.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19.

The source records one amendment, dated 2026-07-27, and it is folded into the
decision above. The original text served security patches only to a valid,
in-entitlement key, which contradicted the published licence commitment that
security fixes are never withheld. The commitment won: the licence gate now
covers feature updates, bugfix updates, and the conformity attestation only.
