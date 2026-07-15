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
import { SearchScreen } from "./search";

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

describe("SearchScreen", () => {
  it("groups hits by type and shows the snippet + relevance provenance", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              type: "person",
              id: "p1",
              title: "Dana Buyer",
              snippet: "…Dana at Acme…",
              score: 0.91,
              trust_tier: "authoritative",
            },
            {
              type: "deal",
              id: "d1",
              title: "Acme expansion",
              snippet: "…platform…",
              score: 0.74,
              trust_tier: "authoritative",
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<SearchScreen q="acme" />);
    await waitFor(() => expect(screen.getByText("People")).toBeTruthy());
    expect(screen.getByText("Deals")).toBeTruthy();
    expect(screen.getByText(/Dana at Acme/)).toBeTruthy();
    // The hit title renders straight from the search result (no per-hit
    // record fetch) as a link to the record's 360.
    const hitLink = screen.getByText("Dana Buyer");
    expect(hitLink.tagName).toBe("BUTTON");
    expect(hitLink.className).toContain("entity-link");
  });

  it("shows an honest empty state", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<SearchScreen q="zzz" />);
    await waitFor(() => expect(screen.getByText(/No matches/)).toBeTruthy());
  });
});
