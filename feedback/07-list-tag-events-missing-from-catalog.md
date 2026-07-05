# feedback/07 — list.*/tag.* events are missing from the events.md closed catalog

**Date:** 2026-07-05 (overnight build session)
**Spec files:** `contract/events.md` §5 (closed catalog, closed verb law), `contract/crm.yaml` (lists/tags operations)
**Build impact:** `modules/collections` (lists & tags), `backend/writeshape_test.go` audit-only waivers

## The defect

The contract defines full CRUD surfaces for lists and tags
(`listLists/createList/archiveList/addListMember`,
`listTags/createTag/archiveTag/applyTag`), and the write-shape rule says
every mutation ships domain row + audit + outbox event in one
transaction. But the events.md §5 closed catalog defines **no
`list.*`/`tag.*` event types**, and §1's closed verb law forbids the
build from inventing catalog types the spec does not enumerate.

So list/tag mutations currently ride the audit-only lane (ratified via
`auditOnlyWrites` in `backend/writeshape_test.go`, each entry pointing
at this file). Downstream consumers — context graph, workflows, read
models, future sync bridges — cannot see list/tag changes on the bus.

## The ask

Either:

1. Add `list.created / list.archived / list.member_added` and
   `tag.created / tag.archived / tag.applied` to the §5 catalog (they
   would route to a family stream per the §1 rule — suggest `person`?
   or a new organizational family note), **or**
2. Ratify in events.md prose that organizational metadata (lists, tags,
   pipeline-like config) is deliberately audit-only in V1.

Whichever way the spec decides, the build removes or keeps the
`auditOnlyWrites` entries accordingly — the fitness test now refuses
any waiver whose filing has been deleted, so resolving this file forces
the code to follow.

## Related drift found on the same surfaces

- `relinkActivity` (crm.yaml) admits `entity_type: lead`, but
  `activity_link` (data-model §7 DDL) has no lead column and its CHECK
  forbids it — the build answers 422 for lead relinks until the spec
  decides which side moves.
- `UpdateActivityRequest.is_done` says completing a task "writes one
  audit row + task.completed event", but the §5 closed catalog has no
  `task.*` family — the build emits `activity.updated` with an
  `is_done` delta.

## Related

The same session also asks (feedback/08) which RBAC objects govern
lists/tags/consent config: features/04 §1's ratified matrix does not
name them.
