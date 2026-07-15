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
