# 06 — Spec README says Change Date is 4 years; the ratified decision is 2 years

## What the spec says

`../margince/README.md:26` (the spec repo's top-level README) describes the
license as:

> converts to Apache-2.0 **four years** after each release (A37/ADR-0029)

## Why it's wrong

The normative business doc, `business/12-license.md`, ratified as A37/ADR-0029,
sets the Change Date at **two years**, repeatedly and explicitly:

- §1 table (line 44): **Change Date = 2 years** from each release's first
  publication.
- §1.1 (line 66): "we hold the Change Date at **2 years** (BUSL allows up to 4)
  precisely to keep FSL's conversion timing and the AI-SEO benefit."
- §3, §3.1, §8: all state the two-year rolling window.

The likely source of the error: the canonical BUSL-1.1 license *body* contains a
built-in outer bound — "the Change Date, **or the fourth anniversary** of the
first publicly available distribution … whichever comes first." That 4-year cap
is BUSL's ceiling, not our chosen Change Date. We set the Change Date parameter to
2 years; the 4-year line in the body is just the never-exceed backstop. The README
appears to have quoted the backstop instead of the ratified parameter.

## Affected spec path(s)

- `../margince/README.md:26` — the "four years" claim.
- (Cross-check) any other doc quoting the conversion window; `12-license.md` is
  correct and should be treated as authoritative.

## What this repo did in the meantime

Shipped `LICENSE` (BUSL-1.1, verbatim body) with **Change Date: 2028-07-04**
(two years from first public availability, 2026-07-04), matching the ratified
A37/ADR-0029 decision, and described the two-year conversion in `README.md`.

## Proposed spec change

Fix `../margince/README.md:26` to read "converts to Apache-2.0 **two years** after
each release (A37/ADR-0029)", matching `business/12-license.md`.
