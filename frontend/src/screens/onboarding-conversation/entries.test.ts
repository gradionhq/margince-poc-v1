/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import { FINDING_EXPAND_EVENT, jumpToFindings } from "./entries";

// The jump's actual contract, independent of any one row's own markup: a
// collapsed row that wants its control focused (not merely scrolled to)
// opts in by listening for `FINDING_EXPAND_EVENT` on its own
// `[data-finding-id]` element and mounting its input in response —
// confirm-card.tsx's FieldRow is the one caller that needs this today, but
// nothing here depends on its internals.

function findingRow(id: string): HTMLLIElement {
  const row = document.createElement("li");
  row.setAttribute("data-finding-id", id);
  row.tabIndex = -1;
  document.body.appendChild(row);
  return row;
}

// Mimics a row that renders its input only once expanded: the state update
// a real React row's setExpanded(true) would trigger is asynchronous, so the
// control is mounted a tick after the event fires, never inside the same
// call stack as the dispatch.
function expandsAsynchronously(
  row: HTMLLIElement,
  tagName: "input" | "textarea",
) {
  row.addEventListener(FINDING_EXPAND_EVENT, () => {
    setTimeout(() => {
      const control = document.createElement(tagName);
      row.appendChild(control);
    }, 0);
  });
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("jumpToFindings", () => {
  it("leaves a collapsed, empty field's input focused and expanded", async () => {
    const row = findingRow("offer_summary");
    expandsAsynchronously(row, "textarea");

    jumpToFindings(["offer_summary"]);

    await vi.waitFor(() => {
      expect(document.activeElement?.tagName).toBe("TEXTAREA");
    });
    expect(row.contains(document.activeElement)).toBe(true);
  });

  it("falls back to focusing the row when it never grows a control", async () => {
    const row = findingRow("website");

    jumpToFindings(["website"]);

    await vi.waitFor(
      () => {
        expect(document.activeElement).toBe(row);
      },
      { timeout: 1000 },
    );
  });

  it("focuses an already-expanded row's input immediately, no expand wait needed", async () => {
    const row = findingRow("icp");
    const input = document.createElement("input");
    row.appendChild(input);

    jumpToFindings(["icp"]);

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(input);
    });
  });

  it("lights the row with the pulse class, then clears it", async () => {
    const row = findingRow("industry");
    const input = document.createElement("input");
    row.appendChild(input);

    jumpToFindings(["industry"]);

    expect(row.classList.contains("ob-conv-pulse")).toBe(true);
    await vi.waitFor(
      () => {
        expect(row.classList.contains("ob-conv-pulse")).toBe(false);
      },
      { timeout: 2500 },
    );
  });
});
