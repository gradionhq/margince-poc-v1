import type { ExtensionScreenRegistry } from "../app/extensions";

// The VANILLA half of the unit-screen registry, resolved through the
// "@composition/screens" alias. See src/screens/ext/index.tsx for the composed
// half and for why the two lanes differ at all.
//
// Empty by construction, and NOT generated — unlike extensions.gen.ts beside
// it, which gen-composition emits and stubMatchesVanilla holds byte-identical
// to its own vanilla output. A screen is core code, so there is nothing here
// for a generator to derive; what the two files share is only the lane
// mechanism.
//
// It is a file rather than an inline `{}` in App.tsx because the alias needs a
// module to resolve to in the lane that composes nothing, and because an
// installation reading this one line learns what its extension screens are.
export const extensionScreens: ExtensionScreenRegistry = {};
