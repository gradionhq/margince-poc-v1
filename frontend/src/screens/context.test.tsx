// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { RecordContextPanel } from "./context";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

describe("RecordContextPanel", () => {
  it("renders assembled sections with an evidence chip", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          anchor: { type: "person", id: "p1" },
          sections: [
            {
              name: "Recent touches",
              items: [
                {
                  ref: { type: "deal", id: "d1" },
                  summary: "Renewal discussion",
                  evidence: [{ snippet: "…renewal…", source: "email:msg-1" }],
                },
              ],
            },
            {
              name: "Related people",
              items: [
                { ref: { type: "person", id: "p2" }, summary: "Dana Buyer" },
              ],
            },
          ],
        }),
      ),
    );
    render(<RecordContextPanel entityType="person" id="p1" />);
    await waitFor(() =>
      expect(screen.getByText("Recent touches")).toBeTruthy(),
    );
    expect(screen.getByText("Related people")).toBeTruthy();
    expect(screen.getByText(/renewal/)).toBeTruthy();
  });

  it("renders a non-linkable item's summary exactly once", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          anchor: { type: "person", id: "p1" },
          sections: [
            {
              name: "Recent touches",
              items: [
                {
                  ref: { type: "activity", id: "a1" },
                  summary: "Called about renewal",
                },
              ],
            },
          ],
        }),
      ),
    );
    render(<RecordContextPanel entityType="person" id="p1" />);
    await waitFor(() =>
      expect(screen.getByText("Recent touches")).toBeTruthy(),
    );
    expect(screen.getAllByText("Called about renewal")).toHaveLength(1);
  });

  // The walk starts at the anchor, so the anchor is inside what comes back, and
  // the server cites every item by its own ref — which the model needs and a
  // reader does not. What must not reach the page: a link to the page the reader
  // is on, a chip proving a record with itself, and a heading over neither.
  it("drops the anchor's own row, and the section that held nothing else", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          anchor: { type: "person", id: "p1" },
          sections: [
            {
              name: "Profile",
              items: [
                {
                  ref: { type: "person", id: "p1" },
                  summary: "Anna Weber",
                  evidence: [{ snippet: "Anna Weber", source: "person:p1" }],
                },
              ],
            },
            {
              name: "Recent touches",
              items: [
                {
                  ref: { type: "deal", id: "d1" },
                  summary: "Renewal discussion",
                  evidence: [{ snippet: "…renewal…", source: "email:msg-1" }],
                },
              ],
            },
          ],
        }),
      ),
    );
    render(<RecordContextPanel entityType="person" id="p1" />);
    await waitFor(() =>
      expect(screen.getByText("Recent touches")).toBeTruthy(),
    );
    expect(screen.queryByText("Profile")).toBeNull();
    expect(screen.queryByText("Anna Weber")).toBeNull();
    // The evidence that is a real source still reaches the reader.
    expect(screen.getByText(/renewal/)).toBeTruthy();
  });

  it("keeps a neighbour's row but not its citation of itself", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          anchor: { type: "person", id: "p1" },
          sections: [
            {
              name: "Related people",
              items: [
                {
                  ref: { type: "person", id: "p2" },
                  summary: "Dana Buyer",
                  evidence: [
                    { snippet: "Dana Buyer", source: "person:p2" },
                    { snippet: "…intro call…", source: "email:msg-9" },
                  ],
                },
              ],
            },
          ],
        }),
      ),
    );
    render(<RecordContextPanel entityType="person" id="p1" />);
    await waitFor(() =>
      expect(screen.getByText("Related people")).toBeTruthy(),
    );
    expect(screen.getByText(/intro call/)).toBeTruthy();
    // The self-citation is the only thing gone: one chip, not two, and the row
    // itself stays — a neighbour is not the anchor.
    expect(screen.queryByText("Dana Buyer")).toBeTruthy();
    expect(
      document.querySelectorAll(".evidence-chip, .chip-evidence"),
    ).toHaveLength(1);
  });

  it("shows the honest empty state when there is nothing related", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ anchor: { type: "person", id: "p1" }, sections: [] }),
      ),
    );
    render(<RecordContextPanel entityType="person" id="p1" />);
    await waitFor(() =>
      expect(screen.getByText("Nothing related yet.")).toBeTruthy(),
    );
  });
});
