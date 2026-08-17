// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Fitness function for the ONE page size.
//
// `ListQuery.perPage` is the size the reader picks in the table footer AND the
// `limit` the fetcher asks the server for. A screen that keeps a literal limit
// instead renders a page size the server never returned: the footer offers 100,
// the response holds 50, and the count line says "1-50 of 100 loaded so far" —
// the exact reading that made the company list look like a broken pager.
//
// Four screens (leads, partners, products, offer templates) carried that
// mismatch for a whole release after the shared wrapper had already been fixed,
// because nothing derived the obligation from the tree. This does: the screen
// list comes from the directory, so a NEW list screen is covered the day it is
// written rather than the day somebody remembers to add it here.
const dir = dirname(fileURLToPath(import.meta.url));

/** Every screen that reads a list through the shared wrapper. */
function listScreens(): string[] {
  return readdirSync(dir)
    .filter((file) => file.endsWith(".tsx"))
    .filter((file) => !file.includes(".test.") && !file.includes(".stories."))
    .filter((file) => {
      const source = readFileSync(resolve(dir, file), "utf8");
      // Calls it, rather than declaring it: listquery.tsx is the wrapper
      // itself and owns no fetcher of its own.
      return (
        /\buseListQuery</.test(source) &&
        !/export function useListQuery/.test(source)
      );
    })
    .sort();
}

describe("every list fetcher asks for the page size the reader picked", () => {
  it("finds the list screens rather than trusting a list written here", () => {
    // The census IS the directory. A hand-kept list would be the thing that
    // went stale, which is the failure this test exists to prevent.
    expect(listScreens()).toContain("people.tsx");
    expect(listScreens()).toContain("organizations.tsx");
    expect(listScreens().length).toBeGreaterThanOrEqual(6);
  });

  it.each(listScreens())("%s reads its limit from the query", (file) => {
    const source = readFileSync(resolve(dir, file), "utf8");
    // A literal limit in the same object that spreads the list query is the
    // mismatch. Other reads on these screens legitimately cap themselves — a
    // typeahead asks for 10 and always should — so only the list fetcher's own
    // `limit` is checked, and it is found by the `cursor` beside it, which no
    // other read on these screens carries.
    const fetcher = source.match(
      /cursor: cursor \|\| undefined,\s*\n\s*limit: ([^,\n]+),/,
    );
    expect(
      fetcher,
      `${file} has no list fetcher shaped like the shared one — if the shape ` +
        `changed, change this check with it rather than deleting it`,
    ).not.toBeNull();
    expect(
      fetcher?.[1],
      `${file} sends a fixed page limit, so the footer's page size and the ` +
        `server's page size can disagree`,
    ).toBe("query.perPage");
  });
});
