# ADR-0006 — Reading the open web happens behind the connector seam

**Status:** Active

**Decided:** 2026-06-04

## The decision

Fetching and parsing a third party's website is a connector, not core code. It
implements the same connector interface every inbound integration implements,
carries the same scopes and the same autonomy tier, and hands its findings to
the capture path rather than writing rows itself.

Every field a fetch produces carries the URL it came from and the text it was
read from. A field with no evidence is dropped, never written with a guess.

A fetch respects the site's robots rules and its crawl delay. When a fetch
fails, the flow falls back to a form a person fills in; onboarding never stalls
on a site that will not be read.

A paid third-party data provider is a separate connector behind the same seam,
not part of the web-reading path.

## Why

Three high-value moments need data that lives outside the customer's mailbox:
reading a prospect's website into a profile, reading a company's legal imprint
instead of retyping an address, and enriching a contact from an email
signature. None of them fit an inbound mailbox connector, and none of them
belong in core.

The evidence rule is what makes the result trustworthy. A CRM that fills fields
from a web page without showing where each value came from trains people to
distrust every field it fills. Requiring the snippet makes the claim checkable
and makes a wrong reading correctable.

Outbound fetching also opens a legal and abuse surface an inbound connector does
not have, which is another reason it sits on the customizable seam under scopes
rather than inside core internals.

## What it binds in this repository

- `backend/internal/shared/ports/connector/connector.go` is the seam. Its
  package comment names the scrape/enrichment connector as one of the
  integrations it covers, and states the rule that the connector normalizes
  while the capture module writes the row, the audit entry and the event.
- `backend/internal/platform/webread/` is the fetching machinery:
  `robots.go` reads the site's rules, `pacer.go` honours the crawl delay,
  `page.go` and `striptags.go` turn a response into text.
- `backend/internal/modules/people/siteread.go` owns the crawl dossier
  (`site_read`): one row per read, advanced by the worker, with at most one read
  per organization in flight so a second click joins the running read.
- `backend/internal/modules/people/coldstartprofile.go` applies the result. It
  writes an evidence row for every accepted field with its `SourceURL`, and it
  fills only empty columns.
- `backend/internal/modules/people/enrich.go` does the same for an organization
  that already exists, stamping the executing principal's provenance.
- `backend/internal/modules/ai/stripper.go` removes credentials from every
  outbound model payload. It is credential hygiene and does not pretend to be a
  PII filter.
- The paid data provider landed as its own connector at
  `backend/internal/modules/integrations/surfe/`, separate from the web-reading
  path exactly as the decision required.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19.

The record was raised by a gap review: the highest-leverage first-run moment in
the product had no home in the architecture at the time. The paid data provider
it deferred has since been built as its own connector.
