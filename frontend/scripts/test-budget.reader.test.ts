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
