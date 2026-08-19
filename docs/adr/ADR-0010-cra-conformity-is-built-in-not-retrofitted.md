# ADR-0010 — Security paperwork the EU Cyber Resilience Act requires ships from day one

**Status:** Active
**Decided:** 2026-06-10

## The decision

Margince produces the conformity artifacts the EU Cyber Resilience Act asks of a
software product, starting now rather than near the December 2027 deadline. Every release
publishes a machine-readable Software Bill of Materials in CycloneDX form. The
repository carries a public vulnerability-disclosure policy, and CI runs
dependency, secret and static scans as blocking gates rather than as advice. The
product is classified as a self-assessment product, so conformity is declared by
Gradion rather than audited by a third party. Conformity attaches to the
artifact
Gradion releases: someone who forks the source and changes it substantially
becomes the manufacturer of their version and owes their own attestation.

## Why

The Cyber Resilience Act regulates products with digital elements sold in the
EU.
The bulk of it, including
the essential requirements and CE marking, applies from 11 December 2027. The
duty to report an actively exploited vulnerability to the EU agency and the
national response teams within 24 hours applies from **11 September 2026** — so
that half is already live, and it reaches products placed on the market before
either date. Dependency hygiene and a disclosure
process are cheap to start and expensive to bolt onto a grown product. Without
this, a release ships with no record of what is inside it, and nobody can answer
which version of which library a reported vulnerability touches.

## What it binds in this repository

- `.github/workflows/sbom.yml` generates the SBOM on release and on any change to
  the SBOM tooling, runs a license gate over it, and signs the output with
keyless
  cosign. `.syft.yaml` configures the generator.
- `SECURITY.md` is the disclosure policy. It routes an exploitable weakness to a
  private GitHub Security Advisory, never to a public issue or pull request.
- `.github/workflows/ci.yml` runs gitleaks as a required secret-scan job, alongside
  the deterministic gates and the `craftsmanship` job.
- `backend/license_test.go` (`TestEveryHandWrittenGoFileCarriesTheLicenseHeader`)
  keeps the BUSL-1.1 SPDX header on every hand-written Go file, which is the
  "do not strip notices" half of honest labeling.
- `CONTRIBUTING.md` and ADR-0046 govern the contribution path that feeds the
  reviewed source this conformity claim rests on.

## History

Adopted from the retired specification, decided 2026-06-10. Rewritten in plain
language 2026-08-19. The source carries an amendment from the same day recording
that a substantial modification transfers the manufacturer obligation to whoever
made it — that rule is stated in the decision above.
