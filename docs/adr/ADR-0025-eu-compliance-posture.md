# ADR-0025 — Margince's AI features are limited-risk, and the compliance evidence ships as one pack

**Status:** Active
**Decided:** 2026-06-17

## The decision

No feature in Margince falls under a high-risk category of the EU AI Act. The
product does not score creditworthiness, evaluate workers, decide eligibility
for an essential service, or process biometrics or emotion. The binding duty
that remains is transparency: disclose when the user is talking to an AI, and
mark content the model drafted or assisted with. Because the customer supplies
the inference credentials, Margince is not a general-purpose AI provider.

GDPR is the regime that actually carries weight here. Scoring a person is
profiling, so every consequential action stays behind a human approval gate and
scores stay advisory — that keeps the product out of the solely-automated-
decision rule. Network-security law is treated as a duty Margince owes as a
supplier, not as a duty on Gradion itself: the customer gets attestable build
evidence, patchable releases, and prompt notice of a relevant incident. All of
that evidence is bundled into one downloadable compliance pack rather than
assembled per customer.

## Why

A product sold on sovereignty and compliance cannot leave its own risk
classification unwritten. An auditor asks one question — show me the evidence —
and a scattered answer costs the sale. Writing the classification down with the
reasoning also forces the honest check that no feature quietly drifted into a
high-risk use.

## What it binds in this repository

- Human approval before a consequential action is the approvals module,
  `backend/internal/modules/approvals/`, wired into the agent surface by
  `backend/internal/compose/`.
- Scores are advisory and explainable rather than opaque; the signals module
  `backend/internal/modules/signals/` holds the scoring and the evidence behind
  it.
- The profiling lawful basis rides the per-purpose consent engine from ADR-0011
  — `consent_purpose` and `person_consent` in
  `backend/migrations/core/0010_consent_retention.up.sql`.
- Statutory retention periods come from a jurisdiction pack, not from the core:
  `backend/internal/shared/ports/jurisdiction/jurisdiction.go` is the seam,
  `extensions/de/de.go` is the German pack. `scripts/check-no-jurisdiction.sh` fails the build if a jurisdiction
  string appears in core code.
- Model calls are metered per call and the payloads are captured only on an
  explicit opt-in — see ADR-0064 and
  `backend/internal/platform/deployconfig/deployconfig.go`.

## History

Adopted from the retired specification, decided 2026-06-17. Rewritten in plain
language 2026-08-19. An amendment on 2026-06-19 settled who holds which
certificate: because Gradion hosts nothing, cloud-service attestations belong
to whoever operates the installation, while the manufacturer duties under the
Cyber Resilience Act stay with Gradion.
