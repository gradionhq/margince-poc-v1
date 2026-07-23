/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ConnectorsCard } from "./connectors";

// The connected-inboxes card makes the onboarding promise ("disconnect in one
// click", "manage in Settings") real. It renders server facts only, and a
// disconnect is confirmed-first before it stops capture.

type CaptureConnection = components["schemas"]["CaptureConnection"];

const gmailConnected: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c1",
  provider: "gmail",
  status: "connected",
  scopes: ["read"],
  last_synced_at: "2026-07-23T09:30:00Z",
};

const gmailStale: CaptureConnection = {
  ...gmailConnected,
  status: "reauth_required",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(connections: CaptureConnection[]) {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/connectors") && request.method === "GET") {
        return jsonResponse({ data: connections });
      }
      if (path.endsWith("/disconnect") && request.method === "POST") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`unstubbed: ${request.method} ${path}`);
    }),
  );
  return calls;
}

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function requestsTo(calls: Request[], suffix: string, method: string) {
  return calls.filter(
    (r) => new URL(r.url).pathname.endsWith(suffix) && r.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the connected-inboxes card", () => {
  it("lists a live connection with its status and last-synced time", async () => {
    stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Gmail")).toBeTruthy();
    expect(screen.getByText("Capturing")).toBeTruthy();
    expect(screen.getByText(/Last synced/)).toBeTruthy();
  });

  it("shows an empty state with a connect CTA when nothing is connected", async () => {
    stubApi([]);
    render(<ConnectorsCard />);
    expect(await screen.findByText(/No inbox is connected yet/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Connect an inbox/ }),
    ).toBeTruthy();
  });

  it("offers reconnect only for a connection that needs re-auth", async () => {
    stubApi([gmailStale]);
    render(<ConnectorsCard />);
    expect(await screen.findByText("Needs reconnect")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reconnect/ })).toBeTruthy();
  });

  it("disconnects only after an explicit confirm", async () => {
    const calls = stubApi([gmailConnected]);
    render(<ConnectorsCard />);
    await screen.findByText("Gmail");

    // Opening the row's disconnect shows a confirm — nothing is called yet.
    await userEvent.click(screen.getByRole("button", { name: /^Disconnect$/ }));
    expect(requestsTo(calls, "/disconnect", "POST").length).toBe(0);

    // The modal's confirm is the one that stops capture.
    const confirms = screen.getAllByRole("button", { name: /^Disconnect$/ });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() =>
      expect(requestsTo(calls, "/disconnect", "POST").length).toBe(1),
    );
  });
});
