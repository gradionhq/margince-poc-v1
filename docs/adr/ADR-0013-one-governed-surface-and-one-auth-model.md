# ADR-0013 — Every caller uses the same governed surface and the same auth

**Status:** Active — the surface and the auth model are built. The named
system-service exceptions are **partly built**; see below.
**Decided:** 2026-06-10

## The decision

There is one Core CRM and one governed surface in front of it. First-party
features are clients of the same tool surface and the same REST contract that a
third party uses, so no internal caller gets a privileged path around the
gates. Every network-reachable call authenticates the same way: an OAuth 2.1
flow that issues an agent seat passport, whose scopes can never exceed the
permissions of the human who granted it. The autonomy tier of an operation is
checked at admission, so the same mutation is gated identically whether it
arrives over the tool transport or over REST. A few enumerated system services
run below the tool layer — the automatic capture writer and database migrations
— and each carries its own audit trail rather than a general privileged path.

## Why

A first-party shortcut around the public surface rots the public surface: the
capability nobody outside can reach stops being tested and stops being real.
Two auth mechanisms means two things to secure, two to audit, and one that
falls behind. Without the transport-agnostic tier check, an agent could take the
REST door and skip the approval a tool call would have staged.

## What it binds in this repository

- `backend/internal/compose/mcpedge.go` mounts the hosted tool transport on the
  same origin as the api. Its own comment records the load-bearing part: the
  tool surface is `srv.toolRegistry`, the same registry the REST agent surface
  composes, so the two transports cannot differ in capability.
- `backend/internal/platform/auth/admit.go` is the single admission point.
  `Gate.Admit` checks scope and autonomy tier together, keyed by operation, so
  the check does not depend on which transport called.
- `backend/internal/modules/identity/oauth*.go` holds the authorization server:
  dynamic client registration, consent, the token exchange, and audience
  binding. There is no second web-auth mechanism beside it.
- `backend/internal/compose/agentpolicy_gen.go` is the generated map from
  contract operation to access class and tier. It is generated from the
  contract, so an operation cannot quietly escape a tier by being added by hand.
- `backend/internal/modules/capture/backfill.go` and the migration runner in
  `backend/cmd/migrate` are the two system services that run below the tool
  layer.

## What is owed

The original decision named a local standard-input transport as a second
transport of the same surface. That transport is gone: a configuration-driven
static passport could not honour revocation the moment a human withdrew it, so
it was removed rather than deferred. Nothing is owed there.

What remains open is the enumeration itself. The record says the system-service
exceptions are explicit wherever the no-backdoor rule is stated. This repository
does not carry a single checked list of them, so a new below-the-gate writer
could be added without anything failing. A test that derives the list from the
tree, in the shape `backend/tableownership_test.go` already uses, is the natural
form; it is **not built**, and its design is not final.

## History

Adopted from the retired specification, decided 2026-06-10. Rewritten in plain
language 2026-08-19.

The source recorded two amendments, both folded into the decision above.
ADR-0055 (2026-07-04) settled that agents keep the full REST surface including
writes, and that the tier check is transport-agnostic rather than tool-only —
REST is not read-only for agents. ADR-0092 (2026-08-07) retired the local
standard-input transport, leaving the hosted HTTP transport as the only inbound
tool surface.
