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
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { organizationQueryKey } from "../screens/organizations";
import { AskFab } from "./fab";

// The FAB is the account's ONE ask surface. The company page used to carry a
// second Ask card of its own, so the same question had two boxes on one
// screen — and only this one is reachable from every tab.

const answer = {
  organization_id: "o-1",
  question: "whats_open",
  generated_at: "2026-06-01T09:00:00Z",
  generated_by: "model",
  sentences: [
    {
      text: "Two open deals, worth about 57000 EUR.",
      evidence: [{ entity_type: "deal", entity_id: "d-1" }],
    },
  ],
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// seed lets a case put the account record in the cache the page would have
// filled, which is where the panel reads the account's NAME from.
function render(ui: ReactNode, seed?: (client: QueryClient) => void) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  seed?.(client);
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const companyRoute = { screen: "companies", id: "o-1" };

async function openPanel() {
  await userEvent.click(screen.getByRole("button", { name: "Ask about this" }));
}

describe("the ask FAB on an account", () => {
  it("asks only the prepared questions, and shows which one the answer answers", async () => {
    let asked: unknown;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        asked = await request.json();
        return jsonResponse(answer);
      }),
    );
    render(<AskFab route={companyRoute} />);
    await openPanel();

    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() => expect(asked).toEqual({ question: "whats_open" }));
    await waitFor(() =>
      expect(
        screen.getByText("Two open deals, worth about 57000 EUR."),
      ).toBeTruthy(),
    );
    // Which writer produced it is never implied.
    expect(screen.getByText("Written by Margince")).toBeTruthy();
    // The question is repeated over its answer, so a reader who has scrolled
    // cannot pair the wrong one with it.
    expect(screen.getAllByText("What's open here?").length).toBeGreaterThan(1);
  });

  it("says there is nothing to answer from rather than nothing at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ ...answer, sentences: [] })),
    );
    render(<AskFab route={companyRoute} />);
    await openPanel();

    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() =>
      expect(screen.getByText(/Nothing here that you can see/)).toBeTruthy(),
    );
  });

  it("reports a failed question instead of leaving the panel blank", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ title: "nope" }, 500)),
    );
    render(<AskFab route={companyRoute} />);
    await openPanel();

    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() =>
      expect(screen.getByText(/could not be answered/)).toBeTruthy(),
    );
  });

  it("names the account it is scoped to, not its id", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(answer)),
    );
    render(<AskFab route={companyRoute} />, (client) => {
      client.setQueryData(organizationQueryKey("o-1"), {
        id: "o-1",
        display_name: "Brandt Automotive GmbH",
      });
    });
    await openPanel();

    // A panel headed by a UUID tells the reader nothing about what it will
    // answer for.
    expect(screen.getByText(/Brandt Automotive GmbH/)).toBeTruthy();
    expect(screen.queryByText(/o-1/)).toBeNull();
  });

  it("falls back to the id when the account is not in the cache yet", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(answer)),
    );
    render(<AskFab route={companyRoute} />);
    await openPanel();

    // Naming the record badly beats naming it wrongly: the panel still says
    // WHICH record it is scoped to rather than silently widening.
    expect(screen.getByText(/o-1/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "What's open here?" }),
    ).toBeTruthy();
  });

  it("offers no account questions away from a record", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(answer)),
    );
    render(<AskFab route={{ screen: "companies" }} />);
    await openPanel();

    // The prepared questions read one account's records. On the list there is
    // no account to read, so they are absent rather than pointed at nothing.
    expect(
      screen.queryByRole("button", { name: "What's open here?" }),
    ).toBeNull();
  });
});
