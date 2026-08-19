# ADR-0102 — A screen renders from server-computed authority, never from role strings

**Status:** Active — the authority object ships and the audit-log route was
narrowed. The named capability flags and the required-field change are
**not built**; see below.

**Decided:** 2026-08-12

## The decision

`GET /me` carries an `authorization` object the server computed: the caller's
licensing seat tier, and its effective object grants merged across every role
the principal holds. A client renders from that object. `roles` stays on the
wire as a display value — what a role badge prints, never what a control
consults.

Both axes must permit an action. The seat is a hard ceiling below role-based
access control, clamped on the HTTP method before any grant is read, and a
client that collapses them into one predicate is wrong in both directions. An
absent grant denies, and an absent `authorization` object denies everything:
read seat, zero grants.

Authority some routes carry is answered by no object grant at all — it is
gated on the admin role, on row scope, or on whether the caller is a human. For
those the server publishes a boolean the client renders on. Each flag answers
one question a screen actually asks, and each is a predicate about this
principal, never a fact about the installation. A boolean describing the
deployment served to every authenticated member tells every sales rep how the
operator configured their system.

Row scope stays off the wire, and no capability is ever answered per record. A
boolean saying "you may edit person X" is an existence oracle for X, and
out-of-scope records answer 404 precisely so existence is not disclosed.

A capability is a rendering input, never a gate. Effective authority is
re-derived server-side on every request, so a caller who forged the whole
payload would gain nothing but a UI that fails on its first call.

## Why

Screens were matching role strings — `roles.includes("admin")` in three
places and `admin || ops` in a fourth — because nothing told them how to learn
what a user may do. Three predicates then drifted apart with nothing holding
them equal.

The drift ran permissive on the server. The full audit log was ratified as
admin-only, but the route gated on unbounded row scope, which admits `admin`,
`ops` and `read_only` alike because all three seed with scope `all`. A
read-only member saw no button, so nobody noticed — and a hidden button is not
a control, so that member could call the route directly and read the whole
governance trail. The opposite direction had already cost twice: a client
predicate wider than the server's renders a control that can only fail.

## What it binds in this repository

- `backend/api/crm.yaml` defines the `Authorization` schema with
  `additionalProperties: false` and `required: [seat_type, objects]`.
  `seat_type` is `full` or `read`; `objects` is keyed by the closed `RbacObject`
  vocabulary and an absent key denies.
- `MeResponse.authorization` references it. `passport` is documented as always
  null and deprecated — an agent is refused at this endpoint.
- `frontend/src/app/capability.ts` is the only place the web client reads
  authority. `useCan` chains through `authorization?.objects?.[object]` so an
  absent object denies at runtime rather than throwing in render.
  `useCanMutate` passes only on an explicit `full` seat.
- `backend/internal/modules/privacy/auditlog.go` gates `ListAuditLog` on human
  principal plus `auth.RequireAdmin`. Its comment states why row scope is the
  wrong predicate: the compliance read is oversight of the operations role's
  own machine-origin actions and cannot sit with the role it oversees.
- `admin_password_link` on `MeResponse` is the pattern this record generalizes:
  true only when the caller holds admin, the installation has no outbound-email
  channel, and a public base URL is configured — a caller capability rather
  than a deployment flag, so no rep learns whether email is configured.
- `data_reset_available` on `MeResponse` replaced `non_production` as the gate
  for the reset action, and `non_production` is now deprecated in the contract:
  a deployment being non-production is not consent to purge its data.

## What is not built

`authorization` is still optional on `MeResponse` rather than required. It
starts optional so a client fails closed against an older server, then becomes
required once every deployment sends it. That second step is owed.

There is no `capabilities` map on the `Authorization` schema and no
`may_read_full_audit` flag in the tree. The route it would describe has already
been narrowed to admin-plus-human, so the server is correct; the client still
reads a role string for it.

`frontend/src/app/capability.ts` keeps two role-string predicates as named
interim exceptions. `useHoldsAdminRole` gates the member roster and the audit
trail. `useHoldsConsentAdminRole` gates the consent purpose registry on
`admin || ops`, and its own comment says why it is temporary: `consent_config`
is a governed object missing from the shipped `RbacObject` vocabulary, so
there is no grant to ask for yet. When it lands, that function becomes
`useCan("consent_config", "read")` and disappears.

Any other surface gating on a role string is non-conforming. The design for
member administration and role-grant editing — an enumerated object grant or a
named capability — is not settled.

## History

Adopted from the retired specification, decided 2026-08-12. Rewritten in plain
language 2026-08-19. The source is marked proposed.

The source names the audit-route narrowing as its first build obligation,
ahead of any client change. That has shipped.

The source cut four drafted capabilities because the specification could not
yet say what they would assert: member administration, the data-subject-request
queue, installed-extension visibility, and data reset. The reset case has since
been answered by `data_reset_available` rather than by a capability flag.
