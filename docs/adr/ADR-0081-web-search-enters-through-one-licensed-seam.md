# ADR-0081 — Web search enters the product through one seam, from a licensed index, with fetching restricted by policy

**Status:** Active
**Decided:** 2026-08-04

## The decision

No feature calls a search API directly. Search enters through one port, with
provider adapters behind it and the provider and its key supplied by
configuration
rather than by code. Only providers serving results from their own index or
under
license qualify — a vendor that resells another engine's results page does not,
and
neither does one whose corpus is built from bulk-collected social profiles,
however
its licence reads. Search may return anything, but what the product fetches is
policy: result metadata (title, snippet, URL, date) is usable as evidence
without
fetching, page fetches go through the existing site-read pipeline with its
robots
handling and caps, and auth-walled social platforms are never fetched. An
installation that configures no provider simply has no search, and the features
that
want it degrade to captured data rather than being silently stubbed.

## Why

The product can read a URL it is given but cannot find a page it was not given,
and
every research capability needs discovery. The field fills that gap two ways: by
reselling broker data whose collection nobody can account for, or by wiring a
search
API to a model and fetching whatever comes back, which drifts into scraping.
Both
break the claim that every stored claim carries a source a buyer can audit.
Putting
the seam in one place is what makes the source policy and the audit trail
enforceable once rather than per feature.

## What it binds in this repository

- `backend/internal/shared/ports/websearch/websearch.go` is the frozen port. `Client`
  is the interface, `Query` the request, and `Result` carries what the provider
  asserts plus a `retrieved_at` this system stamps, so a stored claim can age
  honestly.
- `backend/internal/shared/ports/websearch/sourcepolicy.go` is the fetch policy.
  `MayFetch` returns a `FetchDecision` carrying its reason, and `deniedHosts`
covers
  `linkedin.com`, `xing.com`, `facebook.com`, `instagram.com`, `x.com` and
  `twitter.com`. The host check is suffix-based, so `de.linkedin.com` is denied
like
  `www.linkedin.com`, and the fully-qualified trailing-dot form is normalized
before
  the comparison.
- `backend/internal/platform/websearchhttp/websearchhttp.go` holds the adapters.
  `Brave` binds the Brave Search API as an independent index meeting the
criteria.
  `Disabled` is what a deployment gets when it binds no provider: its
`Provider()`
  answers `"none"` and its `Search` returns nothing, which is the honest-absence
  behaviour rather than a stub.
- The adapter's error paths name the provider and the failure and nothing else,
  because a provider error page echoes the query back.
- `requestTimeout` bounds a single provider call, so a hanging search does not hold
  a request open.

## History

Adopted from the retired specification, decided 2026-08-04. Rewritten in plain
language 2026-08-19. It opened the pre-build gate on the research profiler,
which is
the first consumer of the port. Buying broker-style coverage from a bulk data
vendor
is deliberately out of scope and would need its own decision rather than
arriving as
another provider adapter.
