# 12 — `listLists` / `listTags` return a page shape but define no pagination

**Spec area:** contract/crm.yaml (`GET /lists`, `GET /tags`)

## The gap

Both operations respond with the standard `{data, page}` list envelope, but
neither declares `limit`/`cursor` parameters — unlike every record-data list
(people, deals, list members …) which carries the CAP-PAGE cursor pair. A
client therefore cannot page these catalogs, and a literal implementation is
an unbounded read.

The build treats lists and tags as workspace-curated vocabulary and applies a
hard cap of 1000 rows per read (`collections.catalogCap`), reporting
truncation via `page.has_more=true` with no cursor to continue on.

## Ask

Either add the standard `limit`/`cursor` parameters to both operations, or
state in the contract that these catalogs are bounded vocabulary and name the
bound, so the cap is contractual rather than an implementation choice.

## Status

open
