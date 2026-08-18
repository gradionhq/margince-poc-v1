/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { RestrictedRecordsCard } from "./restrictedrecords";

// Settings → Privacy → Restricted records: the controller sees what a
// statutory obligation is holding, by transaction and deadline, and never the
// correspondence; a role without the retention authority sees why not.

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const HELD_ANGEBOT = {
  activity_id: "00000000-0000-4000-8000-0000000000b1",
  kind: "email",
  occurred_at: "2025-03-04T09:00:00Z",
  restricted_at: "2026-08-18T07:00:00Z",
  restricted_until: "2032-01-01T00:00:00Z",
  reason: "commercial_correspondence · §257 HGB / §147 AO",
  deals: [
    { id: "00000000-0000-4000-8000-0000000000d1", name: "Acme rollout" },
    { id: "00000000-0000-4000-8000-0000000000d2", name: "Acme renewal" },
  ],
  redacted_fields: ["raw", "counterparty_email"],
};

function backend(allow: GrantSpec, records: unknown[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(request.url, "https://test.local");
      const key = `${request.method} ${url.pathname.replace(/^\/v1/, "")}`;
      if (key === "GET /me") {
        return jsonResponse(meFixture({ allow }));
      }
      if (key === "GET /retention/restrictions") {
        return jsonResponse({
          data: records,
          page: { next_cursor: null, has_more: false },
        });
      }
      throw new Error(`unexpected request: ${key}`);
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

describe("RestrictedRecordsCard", () => {
  it("lists what is held, by every qualifying deal, and never the correspondence", async () => {
    backend({ retention_policy: ["read"] }, [HELD_ANGEBOT]);
    render(<RestrictedRecordsCard />);

    expect(await screen.findByText("Acme rollout, Acme renewal")).toBeVisible();
    expect(screen.getByText("Commercial correspondence")).toBeVisible();
    expect(screen.getByText("§257 HGB / §147 AO")).toBeVisible();
    expect(screen.getByText("2 fields removed")).toBeVisible();
    // The wire carries no subject or body, and the card asks for none — but
    // the assertion is about the screen: nothing that reads like the message.
    expect(screen.queryByText(/Angebot/)).not.toBeInTheDocument();
  });

  it("says when nothing is held", async () => {
    backend({ retention_policy: ["read"] }, []);
    render(<RestrictedRecordsCard />);
    expect(await screen.findByText(/No record is being held/)).toBeVisible();
  });

  it("is withheld, not absent, without the retention authority", async () => {
    backend({}, [HELD_ANGEBOT]);
    render(<RestrictedRecordsCard />);
    expect(
      await screen.findByText(/Only an admin or ops can see which records/),
    ).toBeVisible();
    expect(
      screen.queryByText("Acme rollout, Acme renewal"),
    ).not.toBeInTheDocument();
  });
});
