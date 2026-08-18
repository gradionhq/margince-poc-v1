import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { budgetsIn } from "./test-budget";

// The reader's own tests. test-budget.test.ts holds the TREE against the
// reader; nothing there holds the reader against anything, because it can only
// see what this tree happens to contain today. Every shape below was a hole
// this reader actually had — each one made real tests invisible or made their
// budget read smaller than it is, and a smaller budget is a guard that passes.
//
// Synthetic fixtures on purpose: a regression that stops the reader seeing
// `it.each` cannot be caught by a tree whose `it.each` tests all happen to sit
// inside their ceiling.

const GLOBAL = 10_000;
let dir = "";

function read(source: string, name = "probe.test.tsx") {
  const file = join(dir, name);
  writeFileSync(file, source, "utf8");
  return budgetsIn(file, GLOBAL);
}

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "test-budget-"));
});
afterAll(() => {
  rmSync(dir, { recursive: true, force: true });
});

describe("the waiter-budget reader", () => {
  it("takes the default budget for a waiter that states none", () => {
    const [probe] = read(
      `it("x", async () => { await screen.findByText("a"); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(1_000);
    expect(probe?.ceilingMs).toBe(GLOBAL);
    expect(probe?.ceilingIsStated).toBe(false);
  });

  it("sums the waiters a test runs in sequence", () => {
    const [probe] = read(
      `it("x", async () => {
         await screen.findByText("a");
         await waitFor(() => {}, { timeout: 4000 });
         await screen.findAllByRole("row");
       });`,
    );
    expect(probe?.waiterBudgetMs).toBe(6_000);
  });

  it("sees it.each, it.only and it.concurrent, which are tests like any other", () => {
    // Matching only the immediate callee dropped 17 call sites in this tree,
    // silently — and a test the reader never records is a test under no guard.
    const found = read(
      `it.each([1, 2])("each %s", async () => { await screen.findByText("a"); });
       it.only("only", async () => { await screen.findByText("a"); });
       it.concurrent("concurrent", async () => { await screen.findByText("a"); });
       test.each\`a\`("tagged", async () => { await screen.findByText("a"); });`,
    );
    expect(found.map((probe) => probe.name)).toEqual([
      "each %s",
      "only",
      "concurrent",
      "tagged",
    ]);
    expect(found.every((probe) => probe.waiterBudgetMs === 1_000)).toBe(true);
  });

  it("reads the ceiling from either form vitest accepts", () => {
    const [positional] = read(`it("x", async () => {}, 500);`, "a.test.tsx");
    const [options] = read(
      `it("x", { timeout: 500 }, async () => {});`,
      "b.test.tsx",
    );
    expect(positional?.ceilingMs).toBe(500);
    expect(options?.ceilingMs).toBe(500);
    expect([positional?.ceilingIsStated, options?.ceilingIsStated]).toEqual([
      true,
      true,
    ]);
  });

  it("counts the waiters inside a helper the test awaits", () => {
    const [probe] = read(
      `function settle() { return waitFor(() => {}, { timeout: 9000 }); }
       it("x", async () => { await settle(); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(9_000);
  });

  it("counts a one-line arrow helper, whose body IS the waiter", () => {
    // The commonest helper shape, and the one a reader that descends past the
    // node it was handed counts as zero.
    const [probe] = read(
      `const settle = () => waitFor(() => {}, { timeout: 9000 });
       it("x", async () => { await settle(); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(9_000);
  });

  it("counts a helper once per call, not once per test", () => {
    // Recursion protection that also suppressed repeat calls reported 1000 for
    // a test that really spends 4000.
    const [probe] = read(
      `const settle = () => waitFor(() => {}, { timeout: 1000 });
       it("x", async () => { await settle(); await settle(); await settle(); await settle(); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(4_000);
  });

  it("survives a helper that calls itself", () => {
    const [probe] = read(
      `function loop() { return loop(); }
       it("x", async () => { await loop(); await screen.findByText("a"); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(1_000);
  });

  it("folds arithmetic over the constants a file declares", () => {
    const [probe] = read(
      `const POLL = 800;
       const ROUNDS = 2;
       it("x", async () => { await waitFor(() => {}, { timeout: POLL * ROUNDS * 5 }); });`,
    );
    expect(probe?.waiterBudgetMs).toBe(8_000);
  });

  it("folds a constant imported from a sibling module", () => {
    writeFileSync(
      join(dir, "cadence.ts"),
      "export const POLL = 800;\n",
      "utf8",
    );
    const [probe] = read(
      `import { POLL } from "./cadence";
       it("x", async () => { await waitFor(() => {}, { timeout: POLL * 10 }); });`,
      "imports.test.tsx",
    );
    expect(probe?.waiterBudgetMs).toBe(8_000);
    expect(probe?.unresolved).toEqual([]);
  });

  it("resolves the shorthand `{ timeout }`, which is how a named budget is written", () => {
    // The idiomatic spelling once the value has a name, and the one a reader
    // that only looks at property ASSIGNMENTS records as the 1000ms default.
    const [probe] = read(
      `const timeout = 4000;
       it("x", async () => { await waitFor(() => {}, { timeout }); });`,
      "shorthand.test.tsx",
    );
    expect(probe?.waiterBudgetMs).toBe(4_000);
    expect(probe?.unresolved).toEqual([]);
  });

  it("reports an options object it cannot see inside, rather than defaulting", () => {
    // A spread and an options object passed by name each hide a budget. Saying
    // "no timeout stated" about them is the same mistake as folding one wrong:
    // both end as the smallest number that keeps the gate green.
    const spread = read(
      `const opts = { timeout: 4000 };
       it("x", async () => { await waitFor(() => {}, { ...opts }); });`,
      "spread.test.tsx",
    )[0];
    const byName = read(
      `const opts = { timeout: 4000 };
       it("x", async () => { await waitFor(() => {}, opts); });`,
      "byname.test.tsx",
    )[0];
    expect(spread?.unresolved.length).toBe(1);
    expect(byName?.unresolved.length).toBe(1);
  });

  it("reads past the `undefined` placeholder to the options behind it", () => {
    // `findByText(matcher, undefined, { timeout })` is the ordinary spelling
    // when the middle argument is skipped, and a reader that treats any
    // identifier as an opaque options object reports it unreadable instead of
    // folding the budget that is right there.
    const [probe] = read(
      `it("x", async () => {
         await screen.findByText(/a/, undefined, { timeout: 4000 });
       });`,
      "placeholder.test.tsx",
    );
    expect(probe?.unresolved).toEqual([]);
    expect(probe?.waiterBudgetMs).toBe(4_000);
  });

  it("does not read a non-string title as the ceiling", () => {
    // Scanning the arguments from the start finds the title first, and then
    // reports a test's own name as a timeout it cannot fold.
    const [probe] = read(
      `const TITLE = "x";
       it(TITLE, async () => { await screen.findByText("a"); });`,
      "title.test.tsx",
    );
    expect(probe?.unresolved).toEqual([]);
    expect(probe?.ceilingIsStated).toBe(false);
  });

  it("ignores a test vitest never runs", () => {
    // A skipped body spends nothing. Counting it lets a test that cannot fail
    // set the ceiling for the thousands that do.
    const found = read(
      `it.skip("skipped", async () => { await waitFor(() => {}, { timeout: 30000 }); });
       it.todo("todo");`,
      "skipped.test.tsx",
    );
    expect(found).toEqual([]);
  });

  it("counts a helper declared inside the test body once per call", () => {
    // Collected as a helper AND visited where it is defined, it was charged
    // once for the declaration plus once per call.
    const [probe] = read(
      `it("x", async () => {
         const settle = () => waitFor(() => {}, { timeout: 1000 });
         await settle();
         await settle();
       });`,
      "inbody.test.tsx",
    );
    expect(probe?.waiterBudgetMs).toBe(2_000);
  });

  it("takes the EXPORTED constant, not a local one that shares its name", () => {
    // A module-level export and a function-local of the same name are both
    // "the last declaration of X" to a reader that walks a file flat. Taking
    // the wrong one folded a 10000ms budget to 2ms, silently.
    writeFileSync(
      join(dir, "shadowed.ts"),
      `export const POLL_MS = 5000;\nfunction helper() { const POLL_MS = 1; return POLL_MS; }\n`,
      "utf8",
    );
    const [probe] = read(
      `import { POLL_MS } from "./shadowed";
       it("x", async () => { await waitFor(() => {}, { timeout: POLL_MS * 2 }); });`,
      "shadowed.test.tsx",
    );
    expect(probe?.waiterBudgetMs).toBe(10_000);
  });

  it("reports every options shape it cannot see into, not just the two it knows", () => {
    // A whitelist of readable shapes sends everything else down the same path
    // as "states no timeout", and that path ends at the smallest number.
    for (const [name, argument] of [
      ["access", "OPTS.slow"],
      ["call", "makeOpts()"],
      ["ternary", "cond ? { timeout: 30000 } : undefined"],
    ] as const) {
      const [probe] = read(
        `it("x", async () => { await waitFor(() => {}, ${argument}); });`,
        `opaque-${name}.test.tsx`,
      );
      expect([name, probe?.unresolved.length]).toEqual([name, 1]);
    }
  });

  it("records a conditionally-run test, which really does run", () => {
    // `it.runIf(cond)` runs when cond holds. Treating it as skipped left a
    // live test under no guard at all — the more expensive of the two mistakes.
    const found = read(
      `it.runIf(cond)("conditional", async () => { await waitFor(() => {}, { timeout: 30000 }); });`,
      "runif.test.tsx",
    );
    expect(found.map((probe) => probe.waiterBudgetMs)).toEqual([30_000]);
  });

  it("ignores every test inside a skipped suite", () => {
    // isSkipped inspecting only the `it` chain reddened the guard naming a
    // test vitest never runs — the same failure it was added to prevent, one
    // level up.
    const found = read(
      `describe.skip("suite", () => {
         it("x", async () => { await waitFor(() => {}, { timeout: 30000 }); });
       });`,
      "describeskip.test.tsx",
    );
    expect(found).toEqual([]);
  });

  it("does not treat a local variable as a helper just because the name matches", () => {
    // Skipping a "helper declaration" that is not a function at all dropped
    // the waiter inside it.
    const [probe] = read(
      `const renderAs = () => waitFor(() => {}, { timeout: 1000 });
       it("x", async () => {
         const renderAs = await screen.findByText("x", undefined, { timeout: 25000 });
         expect(renderAs).toBeTruthy();
       });`,
      "shadow-local.test.tsx",
    );
    expect(probe?.waiterBudgetMs).toBe(25_000);
  });

  it("reports two same-named helpers only when their budgets disagree", () => {
    // Same cost either way is not an ambiguity that matters; a different cost
    // is, and picking one silently under-counted a whole suite.
    const agree = read(
      `describe("a", () => { const settle = () => waitFor(() => {}, { timeout: 2000 });
         it("x", async () => { await settle(); }); });
       describe("b", () => { const settle = () => waitFor(() => {}, { timeout: 2000 });
         it("y", async () => { await settle(); }); });`,
      "agree.test.tsx",
    );
    const differ = read(
      `describe("a", () => { const settle = () => waitFor(() => {}, { timeout: 8000 });
         it("x", async () => { await settle(); }); });
       describe("b", () => { const settle = () => waitFor(() => {}, { timeout: 2000 });
         it("y", async () => { await settle(); }); });`,
      "differ.test.tsx",
    );
    expect(agree.map((probe) => probe.waiterBudgetMs)).toEqual([2_000, 2_000]);
    expect(differ.every((probe) => probe.unresolved.length === 1)).toBe(true);
  });

  it("folds a ceiling written as an expression, rather than ignoring it", () => {
    // Missed AND unreported, so the test silently rejoined the population it
    // had opted out of.
    const [probe] = read(
      `const SETTLE_MS = 5000;
       it("x", async () => { await screen.findByText("a"); }, SETTLE_MS * 4);`,
      "exprceiling.test.tsx",
    );
    expect(probe?.ceilingMs).toBe(20_000);
    expect(probe?.ceilingIsStated).toBe(true);
  });

  it("reports a timeout it cannot fold instead of assuming the default", () => {
    // The one that matters: an unfoldable budget quietly recorded as 1000 hid a
    // live 11000ms violation from the guard.
    const [probe] = read(
      `import { POLL } from "@somewhere/external";
       it("x", async () => { await waitFor(() => {}, { timeout: POLL * 10 }); });`,
      "unfoldable.test.tsx",
    );
    expect(probe?.unresolved).toEqual(["POLL * 10"]);
  });
});
