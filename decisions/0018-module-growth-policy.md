# 0018 — Module growth policy: when flat stops being the right shape

Date: 2026-07-07. Ratifies the architecture-readability review's first
recommendation: give contributors a rulebook for growing a package BEFORE the
module surface grows further, so splits happen for a reason and not for
symmetry. The core DAG (`shared → platform → modules → compose → cmd`) is
unchanged by this policy, and nothing here loosens ADR-0054 — it turns §3's
prose latitude ("CRUD-heavy modules stay flatter … until a `domain/` is
warranted") into checkable rules.

## The default: flat, with concept-prefixed files

A module package starts flat and stays flat while one person can hold it in
their head. Inside a flat package, a sub-capability lives as concept-prefixed
files — the shape `modules/deals` already uses for offers:

```
offer.go  offer_read.go  offer_lifecycle.go  offer_totals.go
```

This is the sanctioned interim shape. It groups the concept for a reader
without minting a package boundary, exported API, or gate-config change.

## When to split into a subpackage

Split when at least ONE of these is true — and name which one in the PR:

1. **External protocol/provider adapter** — the code speaks a wire protocol or
   third-party surface the rest of the module shouldn't see
   (`capture/imap` is the live example).
2. **Independently testable engine** — a subsystem with its own lifecycle and
   test harness whose internals the parent only drives
   (`agents/runner`).
3. **Policy/ruleset to hide** — data or rules the rest of the repo must not
   import directly (`identity/internal/policy`, decisions/0006;
   `identity/internal/password`).
4. **Generated or mechanically derived code** that would otherwise drown the
   hand-written files.
5. **Stable-API sub-capability** — the boundary already exists in the code
   (narrow call surface, no awkward imports back into the parent) and the
   split makes it legible.

## When NOT to split

- **Not because a noun exists.** `deals/offer`, `deals/product`,
  `people/scoring` (`leadscore*.go`), `people/routing` (`leadrouting*.go`) are
  real concepts and stay as prefixed files until a trigger above fires.
- **Not if the split forces a wide exported API** — a subpackage that needs
  many new exported types just to talk to its parent is a worse shape than the
  flat package. Revert and record why here.
- **Never as a workaround for the no-sibling-import rule.** Cross-module
  behavior stays injected through `internal/compose` or a `shared/ports` seam.

`modules/<name>/internal/…` is reserved for implementation detail that must
not be importable outside that module's subtree; Go enforces it.

## The compose corollary

`internal/compose` groups by the same rules, with one extra guardrail:
compose subpackages hold cross-module ORCHESTRATION only — a compose
subpackage must never become the durable owner of a business entity (that was
the 0015 lesson; domain logic that accretes in compose moves to a module).
The compose root keeps the assembly files: `server.go`, `registry.go`,
`provider.go`, `schema.go`, the generated `stubs_gen.go`/`agentpolicy_gen.go`.

Two compose subpackages exist under this policy:

- **`compose/integration`** — the cross-module integration suites (trigger 2:
  an independently testable harness). A handful of white-box integration tests
  that exercise unexported compose internals remain in the root package; they
  migrate as orchestration splits export their seams.
- **`compose/briefs`** — the Morning-Brief orchestration (trigger 5: the
  rank/score/store/L2 pipeline had a narrow surface already), the pilot for
  the recipe below.

Remaining candidate groups (reporting, exports, enrichment, public surface)
follow the same recipe as separate small moves — each only when touched, none
for symmetry.

## The split recipe (mechanics)

1. Move files as-is first; refactor in a separate commit.
2. Keep the exported API narrow; the parent (or compose root) is usually the
   only caller.
3. Update `.go-arch-lint.yml`: the component's `in:` entry becomes the
   `[path, path/**]` form (identity/capture/agents already use it), and if the
   subpackage may be imported by its parent component, the component must list
   itself in its own `mayDependOn`. depguard and `backend/arch_test.go` derive
   from the tree and need no edit — `TestNoSiblingModuleImports` already
   allows a module's own subpackages.
4. Update any Make target that pins a package path (`bench-perf`, `eval`).
5. Run the full gates (`make check`, `make test-integration`) after each move;
   for test moves, prove count parity:
   `go test -tags integration ./... -list '.*' | grep -c '^Test'` before and
   after must match.

## Spec-side record

Ratified upstream as the ADR-0054 §3 amendment of 2026-07-07 plus the
`B-EP01.*` readability leaves; the store-level authorization explanation this
review also asked for is `docs/explanation/authorization.md`.
