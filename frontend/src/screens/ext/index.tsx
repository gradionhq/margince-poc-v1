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
export const extensionScreens: ExtensionScreenRegistry = {
  "crm-demo": CrmDemoScreen,
};
