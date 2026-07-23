# Add an extension (a stable-tier unit)

For shipping a bounded add-on — today a **jurisdiction pack** — as a named, versioned unit under
`extensions/<name>/`, without editing any upstream-owned file. For *why* the seam is a compile-time
declaration and what the surface guarantees, read
[explanation/extensibility.md](../explanation/extensibility.md) first. For a country pack
specifically, the live capability is retention floors; the running example below builds one.

An extension is its own Go module reaching the core through only the marker-allowlisted
`backend/pkg/**` surface. **Presence under `extensions/` is the enablement** — there is no flag to
flip. `extensions/de` (Germany) and `fixtures/extensions/crm-hello` (the walking-skeleton reference)
are the two units to copy from.

## Scaffold the unit

1. **Create the module directory** `extensions/<name>/` — the directory name is the canonical unit
   name and must match the `Name` you declare. It obeys the grammar `^[a-z0-9]+(-[a-z0-9]+)*$`,
   ≤32 chars (lower-case segments joined by single hyphens); the name keys SQL identifiers and URL
   paths, so anything else is refused at boot.

2. **Add its `go.mod`** — its own module, path `github.com/gradionhq/margince/extensions/<name>`:
   ```text
   module github.com/gradionhq/margince/extensions/<name>

   go 1.26.5
   ```

3. **Write the declaration** `extensions/<name>/<name>.go`, starting with the BUSL SPDX header (every
   hand-written `*.go` file carries it). Export `New() extension.Extension` returning an **inert
   value** — no handle into the core, nothing registered in an `init()`. When the name is hyphenated,
   only the Go **package identifier** drops the hyphen: `crm-hello` uses `package crmhello`, but its
   directory, its module path, and `Extension.Name` all keep the hyphen — a hyphen is illegal in a Go
   identifier, not in a module path:
   ```go
   // SPDX-License-Identifier: BUSL-1.1
   // SPDX-FileCopyrightText: 2026 Gradion

   package fr

   import (
   	"github.com/gradionhq/margince/backend/pkg/extension"
   	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
   )

   func New() extension.Extension {
   	return extension.Extension{
   		Name:          "fr",
   		Version:       "1.0.0",
   		Jurisdictions: []jurisdiction.Pack{pack{}},
   	}
   }

   type pack struct{}

   func (pack) Code() jurisdiction.Code { return "fr" }

   func (pack) Retention() jurisdiction.Retention { return retention{} }

   type retention struct{}

   func (retention) Classes() []jurisdiction.RetentionClass {
   	// Illustrative values only — a real pack's statutory floors and anchors
   	// must be legally verified (French correspondance commerciale ≈ 5 years,
   	// not the German figure).
   	return []jurisdiction.RetentionClass{
   		{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: 5}, Anchor: jurisdiction.AnchorOccurrence},
   	}
   }
   ```
   **Import only `backend/pkg/**` packages carrying `//margince:extension-surface`** — `pkg/extension`
   and `pkg/extension/jurisdiction` today. Any import of `internal/**`, `cmd/**`, an unmarked `pkg`
   package, the composition module, or a sibling extension fails the arch test (the compiler already
   makes `internal/**` unreachable — the test holds the rest).

## Stay inside the declared vocabularies

A jurisdiction pack supplies **policy, never behaviour** — the core retention engine consults it. So
the values you declare must be ones a core engine already understands:

- **`Code`** is a lower-case ISO 3166-1 alpha-2 code, unique across the composed set. A code the `de`
  pack (or any other enabled unit) already holds aborts the boot.
- **`RetentionClassName`** comes from the **closed set** — `commercial_correspondence`,
  `accounting_records`. You supply a *floor* for a known class; you do not invent a class (adding a
  new class kind is a deferred capability that hasn't landed yet). A name outside the set is refused.
- **`Period`** is a calendar span (`{Years: 6}`), never a day count, and every component is
  non-negative — a floor reaches *back*, never forward. Implausibly long spans are refused too
  (`Period.Validate` caps a component at ~1000 years), so a typo can't anchor a cutoff in the far past.
- **`Anchor`** is `occurrence` (the zero value) or `calendar_year_end`. Pick `calendar_year_end` only
  when the statute counts from the year's end (as German §147(4) AO does).

Get the statutory content right — it's legal content, not a default. Pin it with a test (below).

## Write the unit's own test

Each unit is its own Go module, so the backend's `./...` never reaches it — it carries its own tests,
run by `make test-extensions` on the composed workspace. Pin the statutory content so a changed span
or class name is a deliberate, reviewed edit (copy the shape from `extensions/de/de_test.go`):

```go
func TestNewDeclaresTheFloors(t *testing.T) {
	e := New()
	if e.Name != "fr" {
		t.Fatalf("Name = %q, want fr", e.Name)
	}
	// … assert the pack code, class names, and calendar spans.
}
```

A test with no assertion is noise (T11) — assert the actual floors, not just that `New()` returns.

## Compose and verify

Because presence is enablement, the moment the directory exists it's in the enabled set — you only
have to regenerate the composition and run the gates:

1. **`make composition`** — regenerates `build/composition/` from `extensions/`; your unit now appears
   in the generated `Extensions()`. (Every build/test lane depends on this target, so `make check`
   runs it for you; run it directly when you want to inspect the output.)
2. **`make check`** — builds the composed workspace, runs the extension-tier fitness tests
   (import-boundary, marker placement, composition wiring), `make test-extensions` (your unit's own
   tests), and `make check-composition` (a clean regeneration must reproduce `composition.json`
   byte-for-byte).
3. **Boot a role** — `make dev`, then confirm the boot doesn't abort: a duplicate code, an unknown
   class, or a bad period is caught in `RegisterExtensions`' validate phase *before* any surface
   serves, and names the offending unit.

Push only once `make check` is **green** — not red, not still running. The vanilla stub check keeps
passing because it's keyed on the *empty* `extensions/` tree; your unit only changes the composed
output, never the committed `composition/` stub.

## Ship it

**A new unit's directory is gitignored.** `.gitignore` ignores `/extensions/*` except an explicit
allowlist (`!/extensions/de`, …), so a first-party unit you mean to ship in the vanilla tree **must
add its own exception** — `!/extensions/<name>` — or the PR opens with no extension files, and files
you add to the unit later are silently ignored too. (`git add -f` stages the files once but leaves the
directory ignored, so it is not a substitute for the exception.) A purely local, per-installation unit
is *meant* to stay ignored: its presence in the working tree already enables it for that install.

Then commit **the complete unit directory** — every source and test file plus its module metadata
(`go.mod`, and `go.sum` if it carries third-party dependencies) — together with the `.gitignore`
exception. Do **not** commit `build/composition/` — it is generated and ignored — and leave the
tracked `composition/` stub unchanged unless you are deliberately changing the vanilla baseline. Sign
off every commit (`git commit -s`), then the usual PR loop ([CONTRIBUTING.md](../../CONTRIBUTING.md));
merge only when the gates are green.

Two things this how-to does **not** yet cover, because those capabilities haven't landed yet: a unit
owning its own `x_<name>_*` tables (the extension-migration namespace) and its own `/x/<name>/`
HTTP surface. Today an extension contributes policy the core already knows how to apply — a
jurisdiction pack. When those capabilities ship, this guide grows the steps for them.
