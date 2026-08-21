/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { DealBulkBar } from "./dealbulk";

// Where the roster caveat sits in the bulk bar, and why it is not a free choice.
//
// The bar is one wrapping flex row of controls, so a sentence rendered between
// the owner picker and the button that assigns it becomes a flex item BETWEEN a
// control and its verb — and the point the row breaks on a narrow viewport,
// which is where the German string puts it first. A caveat cannot be allowed to
// separate the two halves of one action, so it goes last, and the picker points
// at it by id for a reader who never sees the layout at all.

type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];

// Typed, not asserted: a fixture cast into the contract type can drop a required
// field and still compile, so the test would go on passing after the wire shape
// moved under it.
const DEAL: Deal = {
  id: "d-1",
  name: "Brandt renewal",
  pipeline_id: "p-1",
  stage_id: "s-1",
  status: "open",
  source: "manual",
  captured_by: "human:u-1",
  version: 1,
  created_at: "2026-07-01T08:00:00Z",
  updated_at: "2026-07-01T08:00:00Z",
};

const STAGE: Stage = {
  id: "s-1",
  pipeline_id: "p-1",
  name: "Qualified",
  position: 1,
  semantic: "open",
  win_probability: 40,
};

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * A roster that never stops offering another page, which is the only way this
 * caveat appears at all: the walk stops at its page budget and reports the list
 * as part of one.
 */
function stubTruncatedRoster() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const cursor = url.searchParams.get("cursor");
      const index = cursor ? Number(cursor) : 0;
      return json({
        data: [{ id: `u-${index}`, display_name: `Member ${index}` }],
        page: { next_cursor: String(index + 1), has_more: true },
      });
    }),
  );
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the bulk bar's roster caveat", () => {
  it("comes after every verb, never between the owner picker and the button that applies it", async () => {
    stubTruncatedRoster();
    render(<DealBulkBar deals={[DEAL]} stages={[STAGE]} onDone={() => {}} />);

    const note = await screen.findByText(en["state.partial"]);
    for (const label of [
      en["deals.bulkAssign"],
      en["deals.bulkMove"],
      en["deals.bulkArchive"],
    ]) {
      const verb = screen.getByRole("button", { name: label });
      // DOCUMENT_POSITION_FOLLOWING reads "the note comes after this button in
      // document order", which is the order the flex row lays out and the order
      // a screen reader walks.
      expect(
        verb.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    }
  });

  it("stays attached to the picker it is about, through the picker's description", async () => {
    stubTruncatedRoster();
    render(<DealBulkBar deals={[DEAL]} stages={[STAGE]} onDone={() => {}} />);

    const note = await screen.findByText(en["state.partial"]);
    const picker = screen.getByRole("combobox", {
      name: en["deals.bulkOwner"],
    });

    expect(note.id).not.toBe("");
    expect(picker).toHaveAttribute("aria-describedby", note.id);
  });

  it("does not announce itself as news inside the bar's live region", async () => {
    stubTruncatedRoster();
    render(<DealBulkBar deals={[DEAL]} stages={[STAGE]} onDone={() => {}} />);

    // The bar is `aria-live="polite"`, so the caveat arriving when the walk
    // finishes would be read out mid-interaction — a fact about a list the
    // reader was not asking about, interrupting the one they were.
    expect(await screen.findByText(en["state.partial"])).toHaveAttribute(
      "aria-live",
      "off",
    );
  });
});
