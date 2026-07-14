/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { RecordHistory } from "./history";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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
beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

const created = {
  id: "h1",
  actor_type: "human",
  actor_id: "u1",
  action: "create",
  occurred_at: "2026-07-13T10:00:00Z",
  summary: "Demo Admin created the record",
};
const updated = {
  id: "h2",
  actor_type: "agent",
  actor_id: "sdr",
  on_behalf_of_name: "Anna Weber",
  action: "update",
  occurred_at: "2026-07-14T10:00:00Z",
  summary: "Overnight agent updated the record",
};

describe("RecordHistory", () => {
  it("renders plain-language summaries with attribution", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [created, updated], page: { next_cursor: null } }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText("Demo Admin created the record")).toBeTruthy(),
    );
    expect(screen.getByText("Overnight agent updated the record")).toBeTruthy();
    expect(screen.getByText(/Anna Weber/)).toBeTruthy(); // on_behalf_of_name surfaced
  });

  it("shows an honest empty state", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [], page: { next_cursor: null } }),
      ),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() =>
      expect(screen.getByText(/No changes recorded/i)).toBeTruthy(),
    );
  });

  it("shows an error with retry", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ title: "boom" }, 500)),
    );
    render(<RecordHistory kind="deal" id="d1" />);
    await waitFor(() => expect(screen.getByText(/Retry/i)).toBeTruthy());
  });
});
