# ADR-0019 — The HTTP layer is chi over the standard library, not a web framework

**Status:** Active
**Decided:** 2026-06-04

## The decision

Handlers take the standard library pair — `http.ResponseWriter` and
`*http.Request` — and routing, middleware and URL parameters come from
`go-chi/chi`. There is no framework-specific request context type anywhere in a
handler signature. A server is built by a constructor that takes its
dependencies explicitly and returns an `http.Handler`; route registration is a
generated file, and `main()` is a thin wrapper over a `run(ctx) error`. Handlers
are methods over injected seams, never globals.

## Why

The code generator emits its server interface against the standard library
handler contract, so any other framework would need an adapter layer between the
contract and every handler. A standard-library handler is testable with
`httptest` and carries no framework coupling, which matters for a codebase whose
whole customization story is people editing it. Explicit constructor
dependencies mean a reader can see everything a handler can reach without
searching for package-level state.

## What it binds in this repository

- `github.com/go-chi/chi/v5` is a direct dependency in `backend/go.mod`.
- `backend/internal/contracts/oapi.yaml` sets `chi-server: true`, so the
  generated `backend/internal/contracts/api_gen.go` imports chi and emits the
  chi server interface.
- `backend/internal/compose/routes.go` mounts the contract surface through
  `crmcontracts.HandlerWithOptions(srv, crmcontracts.ChiServerOptions{...})`.
- `backend/internal/platform/httpserver/chassis.go` is the shared chassis every
  process role rides: correlation scope, security headers, panic recovery and
  the health probes. It owns no domain — route assembly lives in the composition
  layer.
- `httpserver.BaseURL` is the single spelling of the `/v1` mount prefix, so a
  module recognizing one of its own routes by address does not hand-copy the
  string from a package it may not import.
- `backend/internal/platform/httperr` writes RFC 7807 problem responses over the
  same standard-library types.
- `backend/internal/compose/server.go` is the constructor shape: `Server` embeds
  every module's handler set and asserts the generated `ServerInterface` itself,
  so a contract operation with no real handler fails at compile time.

## History

Adopted from the retired specification, decided 2026-06-04. Rewritten in plain
language 2026-08-19. This record replaced an earlier stack choice that named a
different HTTP framework for suite consistency with a sibling product; the
divergence is deliberate and bounded to the HTTP edge. The source left the
decision pending a sign-off that has since been given — chi is what the tree
runs.
