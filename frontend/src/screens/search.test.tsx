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

// One result row, so a claim about what a hit does NOT print is made against
// the row rather than against the whole page — the group card and the search
// field are not places a badge or a figure would have appeared.
function hitRow(container: HTMLElement): HTMLElement {
  const row = container.querySelector<HTMLElement>(".search-hit");
  if (!row) {
    throw new Error("the results rendered no hit row at all");
  }
  return row;
}

describe("SearchScreen", () => {
  it("groups hits by type and shows the snippet", async () => {
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

  // A stored record is `authoritative` in native mode — every one of them — so
  // a badge for that tier put the same pill on every row on the page and told a
  // reader nothing they did not already assume. The score is not drawn either:
  // the contract bounds it to nothing, so the retriever's raw figure reached the
  // page as a percentage over 100 that a reader can neither act on nor doubt.
  it("prints neither a tier badge nor a relevance figure for a stored record", async () => {
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
              // Past 1, the way the seeded retriever actually scores: a hit that
              // renders its score as a percentage reads "relevance 280%" here.
              score: 2.8,
              trust_tier: "authoritative",
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    const { container } = render(<SearchScreen q="acme" />);
    await waitFor(() => expect(screen.getByText("Dana Buyer")).toBeTruthy());
    const row = hitRow(container);
    // What the row still carries: the name and the matched text.
    expect(row.textContent).toContain("Dana at Acme");
    expect(row.querySelectorAll(".badge")).toHaveLength(0);
    expect(screen.queryByText("verified")).toBeNull();
    expect(row.textContent).not.toContain("%");
    expect(row.textContent).not.toMatch(/relevance/i);
  });

  it("badges an external-tier hit as mirrored", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              type: "person",
              id: "p1",
              title: "Dana Buyer",
              trust_tier: "external",
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    const { container } = render(<SearchScreen q="acme" />);
    await waitFor(() => expect(screen.getByText("Dana Buyer")).toBeTruthy());
    // The tier covers every overlay and connector source, so the badge names
    // none of them: a hit carries no provider field, and a vendor name here
    // would be stamped on rows mirrored from a different system.
    expect(screen.getByText("from a connected system")).toBeTruthy();
    expect(screen.queryByText(/HubSpot/)).toBeNull();
    // authoritative's badge never renders alongside a mirrored hit.
    expect(screen.queryByText("verified")).toBeNull();
    // And it is the ONLY badge on the row: the mirrored mark is the one that
    // varies between hits, so a second pill beside it is what buried it.
    expect(hitRow(container).querySelectorAll(".badge")).toHaveLength(1);
  });

  // A tier the record CARRIES and the page does not draw reads as a record with
  // nothing to declare, which is the opposite of what unverified means.
  it("badges an unverified hit rather than leaving it unmarked", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              type: "person",
              id: "p2",
              title: "Sam Unknown",
              trust_tier: "unverified",
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    render(<SearchScreen q="acme" />);
    await waitFor(() => expect(screen.getByText("Sam Unknown")).toBeTruthy());
    expect(screen.getByText("unverified")).toBeTruthy();
    expect(screen.queryByText("verified")).toBeNull();
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
