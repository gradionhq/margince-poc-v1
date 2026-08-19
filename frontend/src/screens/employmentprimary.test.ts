import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// `is_current_primary` is the SERVER's to decide when a request omits it — a
// person's only current employment becomes their current primary one. A screen
// that states the field by hand takes that decision away, and the two screens
// that did it were the ones a real user reaches: the person rail, which sent an
// untouched checkbox's `false`, and the generic relationship modal, which sent a
// literal for every kind while offering no primary control at all. Between them
// the rule could not fire from anywhere a person actually works.
//
// Derived from the tree rather than from a list of the two known offenders: the
// literal is one line and trivial to reintroduce on a screen that does not exist
// yet, and nothing else would notice.
describe("is_current_primary is not stated by hand", () => {
  const dir = join(import.meta.dirname, ".");

  function screenSources(): Array<{ name: string; body: string }> {
    return readdirSync(dir)
      .filter(
        (f) =>
          f.endsWith(".tsx") &&
          !f.includes(".test.") &&
          !f.includes(".stories."),
      )
      .map((name) => ({ name, body: readFileSync(join(dir, name), "utf8") }));
  }

  it("finds no screen sending a literal", () => {
    // A LITERAL only. `is_current_primary: isCurrent ?? undefined` is the person
    // rail passing on what the reader actually did, which is the whole point —
    // a gate that banned the key outright would ban saying "yes" too.
    const literal = /is_current_primary:\s*(true|false)\b/;
    const offenders = screenSources()
      .filter((file) => literal.test(file.body))
      .map((file) => file.name);

    expect(offenders).toEqual([]);
  });

  it("catches one when it is there", () => {
    // The gate above passes on an empty set as readily as on a clean tree, so
    // this proves the pattern actually matches the shape it forbids.
    const literal = /is_current_primary:\s*(true|false)\b/;
    expect(literal.test("  is_current_primary: false,")).toBe(true);
    expect(literal.test("  is_current_primary: isCurrent ?? undefined,")).toBe(
      false,
    );
  });
});
