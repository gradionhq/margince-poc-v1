/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

type Sent = { key: string; body: unknown };

function backend(allow: GrantSpec, records: unknown[]): Sent[] {
  const sent: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const url = new URL(request.url, "https://test.local");
      const key = `${request.method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (request.method !== "GET") {
        body = await request.json().catch(() => null);
      }
      sent.push({ key, body });
      if (key === "GET /me") {
        return jsonResponse(meFixture({ allow }));
      }
      if (key === "GET /retention/restrictions") {
        return jsonResponse({
          data: records,
          page: { next_cursor: null, has_more: false },
        });
      }
      if (
        request.method === "POST" &&
        key.startsWith("/retention/restrictions")
      ) {
        return new Response(null, { status: 204 });
      }
      throw new Error(`unexpected request: ${key}`);
    }),
  );
  return sent;
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

  it("releases a held record only with a stated reason, and says the release erases", async () => {
    const sent = backend({ retention_policy: ["read", "update"] }, [
      HELD_ANGEBOT,
    ]);
    const user = userEvent.setup();
    render(<RestrictedRecordsCard />);

    await user.click(await screen.findByRole("button", { name: "Release" }));
    // The reason is what makes it a decision rather than a toggle, so the
    // confirm is inert until one is typed.
    const confirm = screen.getByRole("button", { name: "Release and erase" });
    expect(confirm).toBeDisabled();
    expect(screen.getByText(/Releasing ERASES the record/)).toBeVisible();

    await user.type(
      screen.getByRole("textbox", { name: /Why/ }),
      "wrongly classified: marketing enquiry",
    );
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    const release = sent.find((call) => call.key.endsWith("/release"));
    expect(release?.body).toEqual({
      reason: "wrongly classified: marketing enquiry",
    });
  });

  it("offers no decision to a role that may read the list but not decide", async () => {
    backend({ retention_policy: ["read"] }, [HELD_ANGEBOT]);
    render(<RestrictedRecordsCard />);
    expect(await screen.findByText("Acme rollout, Acme renewal")).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "Release" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Pin a record" }),
    ).not.toBeInTheDocument();
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
