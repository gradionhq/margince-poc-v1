/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { LinkedInReachCard, LinkedInReviewCard } from "./linkedin-review";

// The review queue and the reach table. What is worth pinning is what would
// quietly mislead somebody working through thirty rows: confirming a guess the
// server never made, a decided row that stays on the list, and a truncated
// reach table that reads as the whole network.

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

type Call = { method: string; path: string };

function stubRoutes(routes: Record<string, unknown>) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const path = url.pathname.replace(/^\/v1/, "");
      calls.push({ method, path });
      const key = `${method} ${path}`;
      if (key in routes) {
        return jsonResponse(routes[key]);
      }
      return jsonResponse({ title: "not found" }, 404);
    }),
  );
  return calls;
}

const SUGGESTION = {
  id: "c-1",
  full_name: "Andreas Müller",
  position: "Geschäftsführer",
  company_name: "SIMIO GmbH & Co. KG",
  email: null,
  connected_on: "2021-04-02",
  match_status: "suggested",
  matched_person_id: "p-1",
  matched_person_name: "A. Müller",
  matched_org_id: null,
  matched_org_name: null,
};

const EMPTY_PAGE = { next_cursor: null, has_more: false };

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("LinkedInReviewCard", () => {
  it("shows the connection's own name and employer, which is what the guess is judged on", async () => {
    stubRoutes({
      "GET /me/linkedin-connections": { data: [SUGGESTION], page: EMPTY_PAGE },
    });
    render(<LinkedInReviewCard />);
    const row = await screen.findByRole("listitem");
    expect(within(row).getByText("Andreas Müller")).toBeInTheDocument();
    expect(
      within(row).getByText(/Geschäftsführer · SIMIO GmbH & Co. KG/),
    ).toBeInTheDocument();
    // And the contact it is guessed to be, or a rep cannot tell what they are
    // agreeing to.
    expect(within(row).getByText("A. Müller")).toBeInTheDocument();
  });

  it("refuses to confirm a suggestion that names no contact", async () => {
    // The matcher found an employer but nobody to link to. Confirming would ask
    // the server to link this connection to nothing, so the action is not
    // offered — rejecting still is, because clearing it is a real decision.
    stubRoutes({
      "GET /me/linkedin-connections": {
        data: [{ ...SUGGESTION, matched_person_id: null, matched_person_name: null }],
        page: EMPTY_PAGE,
      },
    });
    render(<LinkedInReviewCard />);
    const row = await screen.findByRole("listitem");
    expect(
      within(row).getByRole("button", { name: /that is them/i }),
    ).toBeDisabled();
    expect(
      within(row).getByRole("button", { name: /not them/i }),
    ).toBeEnabled();
  });

  it("names no record when the suggested contact is outside the member's row scope", async () => {
    // The row is still shown so the member can clear it. The contact is NOT
    // named — that is exactly what the person read closes, and a review queue
    // must not be the side door around it.
    stubRoutes({
      "GET /me/linkedin-connections": {
        data: [{ ...SUGGESTION, matched_person_name: null }],
        page: EMPTY_PAGE,
      },
    });
    render(<LinkedInReviewCard />);
    expect(
      await screen.findByText(/contact you cannot see/i),
    ).toBeInTheDocument();
  });

  it("re-reads the queue after a decision so a decided row leaves the list", async () => {
    // Without the invalidation a member confirms a row, watches it sit there,
    // and confirms it again.
    const calls = stubRoutes({
      "GET /me/linkedin-connections": { data: [SUGGESTION], page: EMPTY_PAGE },
      "POST /me/linkedin-connections/c-1/confirm": {
        connection: { ...SUGGESTION, match_status: "confirmed" },
        profile_url_written: true,
      },
      "GET /me/linkedin-reach": {
        accounts: [],
        accounts_total: 0,
        unresolved_connections: 0,
      },
    });
    render(<LinkedInReviewCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /that is them/i }),
    );
    await waitFor(() => {
      const listReads = calls.filter(
        (c) => c.method === "GET" && c.path === "/me/linkedin-connections",
      );
      expect(listReads.length).toBeGreaterThan(1);
    });
  });

  it("says the queue is clear rather than rendering nothing", async () => {
    stubRoutes({
      "GET /me/linkedin-connections": { data: [], page: EMPTY_PAGE },
    });
    render(<LinkedInReviewCard />);
    expect(
      await screen.findByText(/every suggestion has been decided/i),
    ).toBeInTheDocument();
  });
});

describe("LinkedInReachCard", () => {
  it("shows the gap between who you know and who is on file", async () => {
    // The gap IS the finding: eleven people you know at this account, three of
    // them contacts. Rendering only the total would hide the other eight.
    stubRoutes({
      "GET /me/linkedin-reach": {
        accounts: [
          {
            organization_id: "o-1",
            display_name: "Nfq",
            connections: 11,
            contacts_on_file: 3,
          },
        ],
        accounts_total: 1,
        unresolved_connections: 0,
      },
    });
    render(<LinkedInReachCard />);
    const row = await screen.findByRole("row", { name: /Nfq/ });
    expect(within(row).getByText("11")).toBeInTheDocument();
    expect(within(row).getByText("3 of 11")).toBeInTheDocument();
  });

  it("says how much it is not showing rather than reading as the whole network", async () => {
    // A truncated table presented as complete understates reach, which is the
    // one thing this view exists to state.
    stubRoutes({
      "GET /me/linkedin-reach": {
        accounts: [
          {
            organization_id: "o-1",
            display_name: "Nfq",
            connections: 11,
            contacts_on_file: 3,
          },
        ],
        accounts_total: 267,
        unresolved_connections: 4797,
      },
    });
    render(<LinkedInReachCard />);
    expect(
      await screen.findByText(/Showing 1 of 267 accounts/),
    ).toBeInTheDocument();
    expect(screen.getByText(/4797 connections work somewhere/)).toBeInTheDocument();
  });
});
