# 0015 — The privacy module and the table-ownership fitness gate

Date: 2026-07-05. Structural close-out of the review finding that
`internal/compose` had accreted ~600 lines of first-class GDPR domain logic
(erasure, SAR assembly, the retention engine) while being documented as "the
wiring".

## The privacy module

- **`internal/modules/privacy`** now owns the GDPR engines from 0013:
  `Eraser`/`ErasePerson` (Art. 17), `AssembleSAR` (Art. 15), and
  `RetentionService`/`RunRetention` — the module ADR-0054 §1 reserved as
  "gdpr", named `privacy`. Everything in 0013's GDPR-arm record holds; only
  the package moved (`compose.Eraser` → `privacy.Eraser`, etc.).
- The DSR case queue (rows + HTTP surface) **stays in consent**; compose
  injects `privacy.NewEraser` into consent's DSR handlers exactly as it
  injected compose's own Eraser before — the modules still never import each
  other. `cmd/worker` ticks `privacy.RunRetention` directly (cmd may import
  modules, same as its `search`/`ai` edges).
- The engines' cross-module SQL (anonymizing person/lead rows, purging
  capture/embedding rows) **stays raw SQL inside privacy**: a data-subject
  obligation must reach every store holding the subject in ONE transaction
  per record — the 0011 single-transaction exception; routing each purge
  through the owning module's API would trade the atomicity that IS the
  guarantee for boundary hygiene.
- The erasure/retention/SAR integration suites remain in compose (they are
  cross-module end-to-end suites, compose's charter); the writeshape waiver
  for `AssembleSAR` moved to its new key.

## The table-ownership gate

The import DAG was enforced three ways, but table ownership at the SQL layer
was enforced zero ways. **`backend/tableownership_test.go`** closes that: it
AST-walks hand-written Go under `internal/modules` + `internal/compose`,
extracts INSERT/UPDATE/DELETE targets from SQL string literals (plus the
`storekit.Patch.Apply` table argument), and asserts each module writes only
tables it owns per a declared table→owner map. Cross-store writes (people's
merge relinks, capture's ingest materialization, privacy's erasure, the
direct audit/outbox writers in identity/approvals) are explicit waivers keyed
`module:table`, each with a mandatory self-contained rationale; empty
rationales and stale waivers fail. SELECTs are out of scope — reads are
governed by RLS + the platform/auth row-scope clauses.
