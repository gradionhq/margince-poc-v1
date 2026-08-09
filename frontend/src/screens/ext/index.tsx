import type { ExtensionScreenRegistry } from "../../app/extensions";
import { CrmDemoScreen } from "./crmdemo";

// The COMPOSED half of the unit-screen registry: which in-tree screen renders
// for which composed unit.
//
// Resolved through the "@composition/screens" alias, which is the third and
// last member of the two-lane pair tsconfig.app.json already carries. The
// VANILLA lane resolves src/composition/extscreens.ts — an empty registry —
// and the COMPOSED lane resolves this file. Same mechanism as
// "@composition/extensions" (the descriptor registry) and
// "@composition/schema" (the merged contract's types), and it exists for the
// same reason: a unit screen calls its unit's routes, and those routes are in
// `paths` only in the lane whose installation serves them. Committing this
// import into the vanilla program would fail `tsc -b` on every extension call
// site, which is the CORRECT outcome for code that has no installation to run
// on — so the vanilla program does not carry it.
//
// A unit ships no TSX. These screens are CORE files, reviewed like every other
// core file; extensions/<name>/frontend/ is still refused on sight by
// gen-composition's scan, and lifting it means bundling unit-authored TSX into
// the SPA. This registry is therefore hand-written and NOT generated — which is
// also what keeps the empty-tree byte-identity gate untouched by it.
//
// A composed unit with no entry here is not an error: App.tsx falls back to the
// generic published-operations card, which is what every unit without a
// bespoke screen (de, yogi, crm-hello) renders.
//
// THE REVERSE IS AN ERROR, and it is the cost of keeping these screens in core.
// An entry here for a unit the installation does NOT compose leaves a file that
// calls routes the merged contract no longer carries, so
// `make fe-typecheck-composed` fails — which is the gate doing its job, but it
// means removing a unit is a removal in TWO places until
// extensions/<name>/frontend/ is a real capability layer:
//
//	git rm -r extensions/<name> frontend/src/screens/ext/<name>.tsx …
//	# and drop its line from this map, then:
//	pnpm -C frontend exec biome check --write src/screens/ext/index.tsx
//
// That last step is not tidiness: removing the LAST entry leaves `= {\n};`,
// and the formatter wants `= {}` — without it `check-fe` fails on formatting
// alone, on a removal that is otherwise complete.
//
// TWO places, and verified to be exactly two: docs/how-to/add-an-extension.md
// carries the same recipe and it was run end to end against crm-demo with
// `make check-q` green. It briefly WAS three — gen-composition's namespace-wall
// fixture pairing hard-coded this unit's path and failed on its absence — which
// is fixed there rather than documented here, because removing a unit must not
// require editing the core's tests.
//
// Found by Task 14's UAT re-run of the removal leg. Documented rather than
// worked around: a conditional include is not expressible in tsconfig, and
// hiding the breakage would mean shipping a screen whose routes are gone.
export const extensionScreens: ExtensionScreenRegistry = {
  "crm-demo": CrmDemoScreen,
};
