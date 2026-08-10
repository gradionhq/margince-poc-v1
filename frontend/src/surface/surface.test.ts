// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The published frontend surface is a CONTRACT, and this is what keeps it one.
//
// Its Go counterpart is the marker-placement fitness test: there, the compiler
// makes internal/** unreachable and the test holds the rest. Here the compiler
// holds nothing — a bundler resolves whatever a path can reach — so the
// surface is exactly two things: this exports map, and the gate that refuses a
// unit importing past it. Widening the map is a reviewed act; widening it by
// accident, because some tool wanted a path, is the failure this test exists
// to make impossible.

import { describe, expect, it } from "vitest";
import pkg from "../../package.json" with { type: "json" };
import * as apiSurface from "./api";
import * as designSystem from "./design-system";
import * as appSurface from "./index";

describe("the published frontend surface", () => {
  it("publishes exactly three subpaths", () => {
    expect(Object.keys(pkg.exports).sort()).toEqual([
      "./api",
      "./app",
      "./design-system",
    ]);
  });

  // The names, not just the count. A re-export file that quietly lost a symbol
  // would still satisfy the map above while breaking every unit that imported
  // it — and a unit's screen is compiled in a lane a core-only change does not
  // run, so nothing else would notice until an installation did.
  it("publishes exactly these names", () => {
    expect(Object.keys(designSystem).sort()).toEqual([
      "Badge",
      "Button",
      "Card",
      "EmptyState",
      "Field",
      "SectionHeader",
      "TextInput",
    ]);
    expect(Object.keys(apiSurface).sort()).toEqual([
      "QueryStates",
      "api",
      "throwProblem",
    ]);
    expect(Object.keys(appSurface).sort()).toEqual([
      "LocaleProvider",
      "formatDateTime",
      "useCan",
      "useCanWrite",
      "useLocale",
      "useT",
    ]);
  });
});
