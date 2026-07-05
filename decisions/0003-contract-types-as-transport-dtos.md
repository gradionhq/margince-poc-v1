# 0003 — Generated contract types are the transport DTOs; provenance is server-stamped

Status: accepted · 2026-07-03

**Contract types as DTOs.** Module stores and handlers speak the
oapi-codegen-generated `crmcontracts` types directly instead of
maintaining a parallel hand-written domain-struct layer plus mappers.
Rationale: `crmcontracts` is a dependency-free generated artifact every
module may import (architecture/01); data-model.md declares the domain
structs *are* the schema, and crm.yaml mirrors the schema field-for-field
— so a second struct set would duplicate the same shape twice and
reintroduce the drift the contract pipeline exists to kill (the
poc red-team's H4: 111 hand-rolled duplicate types). Where domain
behavior needs richer types than the wire shape, the store introduces
them locally; the wire shape never leaks *into* SQL semantics.

**Server-stamped `captured_by`.** The contract's create requests carry a
required client-supplied `captured_by`. We deliberately ignore the client
value and stamp the authenticated principal (`crmctx.Actor`) on every
write: provenance that a client can assert is provenance that can be
forged, which would poison the P5 auto-capture metric and the audit
trail. Logged upstream as `fable feedback/09`; if the spec later ratifies
client-supplied provenance for trusted connectors, that arrives as an
explicit allow-list, not a default.

**501 completeness floor.** All 81 contract operations are mounted from
day one; unimplemented ones answer an explicit `501 not_implemented`
problem, so the contract surface can never silently 404 and coverage is
observable by calling it.
