// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Fitness function for the overlay-mode refusal set (backend/internal/
// compose/overlaywrite.go's overlayRecordWriteTools + overlayread.go's
// unsupportedOverlayParam): a SPA call site that can receive
// `unsupported_by_sor` (a refused WRITE) or `unsupported_in_overlay_mode`
// (a refused READ dial) from one of these ops must hand problemMessage/
// throwProblem a translator, or the caller falls back to the server's terse
// sentinel text instead of the copy in screens/common.tsx naming which kind
// of refusal happened. Most of these ops are also hidden client-side behind
// `!overlay` (create.tsx, merge.tsx, DealBadges, …), so the untranslated
// fallback is latent, not a live user-facing bug — but it fires the moment
// a workspace's overlay mode flips mid-session and a stale ["me"] cache
// still shows the affordance (task-6 review round 2). This test does not
// discover a NEW refused call site by itself: it pins the swept set below,
// so an edit that drops the `t` argument from one of these lines — the
// exact way three of them were missed the first time — fails loudly here
// instead of only in a stale-cache bug report. The swept set (11 ops per
// overlaywrite.go, minus DELETE /activities/{id}, which no SPA screen
// calls):
//   create person/org/deal/lead, log-activity (POST /activities, both its
//   logactivity.tsx and tasks.tsx callers), advance-deal (both its board
//   and reopen callers), merge-person, merge-org, promote-lead,
//   disqualify-lead — plus tasks.tsx's GET /activities?kind=task read,
//   which hits the READ-side unsupported_in_overlay_mode this same fitness
//   check would otherwise miss.
const dir = dirname(fileURLToPath(import.meta.url));

function source(file: string): string {
  return readFileSync(resolve(dir, file), "utf8");
}

// Finds the Nth (1-indexed) occurrence of `anchor` — the endpoint call's own
// literal path — then the nearest `if (error)` after it, and asserts the
// throw inside that block passes a translator. Anchoring on the endpoint
// path (not line numbers) survives reformatting; failing loudly if the
// anchor or the `if (error)` drift apart catches a rewritten call site
// before it ships silently untranslated.
function assertTranslatedRefusal(
  file: string,
  anchor: string,
  label: string,
  occurrence = 1,
) {
  const text = source(file);
  let anchorIndex = -1;
  for (let seen = 0; seen < occurrence; seen++) {
    anchorIndex = text.indexOf(anchor, anchorIndex + 1);
  }
  expect(
    anchorIndex,
    `${label}: anchor ${JSON.stringify(anchor)} (occurrence ${occurrence}) not found in ${file}`,
  ).toBeGreaterThanOrEqual(0);
  const errorIndex = text.indexOf("if (error)", anchorIndex);
  expect(
    errorIndex,
    `${label}: no "if (error)" found after its endpoint call in ${file}`,
  ).toBeGreaterThanOrEqual(0);
  expect(
    errorIndex - anchorIndex,
    `${label}: "if (error)" is implausibly far from its anchor in ${file} — the anchor likely matched the wrong call`,
  ).toBeLessThan(600);
  const throwSite = text.slice(errorIndex, errorIndex + 150);
  expect(
    /throwProblem\(error,\s*t\)|problemMessage\(error,\s*t\)/.test(throwSite),
    `${label}: this refusal does not pass a translator (t) — it will show the ` +
      "server's raw sentinel code instead of overlay.refused/overlay.filterUnsupported",
  ).toBe(true);
}

describe("overlay refusal copy — translator coverage", () => {
  it("create-person (POST /people)", () => {
    assertTranslatedRefusal(
      "people.tsx",
      'api.POST("/people", {',
      "create-person",
    );
  });

  it("merge-person (POST /people/{id}/merge)", () => {
    assertTranslatedRefusal(
      "people.tsx",
      '"/people/{id}/merge"',
      "merge-person",
    );
  });

  it("create-org (POST /organizations)", () => {
    assertTranslatedRefusal(
      "organizations.tsx",
      'api.POST("/organizations", {',
      "create-org",
    );
  });

  it("merge-org (POST /organizations/{id}/merge)", () => {
    assertTranslatedRefusal(
      "organizations.tsx",
      '"/organizations/{id}/merge"',
      "merge-org",
    );
  });

  it("create-deal (POST /deals)", () => {
    assertTranslatedRefusal("deals.tsx", 'api.POST("/deals", {', "create-deal");
  });

  it("advance-deal — the board's own advance (POST /deals/{id}/advance, 1st caller)", () => {
    assertTranslatedRefusal(
      "deals.tsx",
      'api.POST("/deals/{id}/advance", {',
      "advance-deal (board)",
      1,
    );
  });

  it("advance-deal — ReopenAction's advance (POST /deals/{id}/advance, 2nd caller)", () => {
    assertTranslatedRefusal(
      "deals.tsx",
      'api.POST("/deals/{id}/advance", {',
      "advance-deal (reopen)",
      2,
    );
  });

  it("create-lead (POST /leads)", () => {
    assertTranslatedRefusal("leads.tsx", 'api.POST("/leads", {', "create-lead");
  });

  it("promote-lead (POST /leads/{id}/promote)", () => {
    assertTranslatedRefusal(
      "leads.tsx",
      '"/leads/{id}/promote"',
      "promote-lead",
    );
  });

  it("disqualify-lead (DELETE /leads/{id})", () => {
    assertTranslatedRefusal(
      "leads.tsx",
      'api.DELETE("/leads/{id}", {',
      "disqualify-lead",
    );
  });

  it("log-activity from a 360's LogActivity form (POST /activities)", () => {
    assertTranslatedRefusal(
      "logactivity.tsx",
      'api.POST("/activities", {',
      "log-activity (logactivity.tsx)",
    );
  });

  it("log-activity from the Tasks screen's create (POST /activities)", () => {
    assertTranslatedRefusal(
      "tasks.tsx",
      'api.POST("/activities", {',
      "log-activity (tasks.tsx create)",
    );
  });

  it("the Tasks screen's kind=task read (GET /activities) — the READ-side refusal", () => {
    assertTranslatedRefusal(
      "tasks.tsx",
      'api.GET("/activities", {',
      "tasks list read",
    );
  });
});
