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
import { HomeScreen } from "./home";

// Home / Morning Brief acceptance: the /brief run IS the queue (ranked items
// with the §10.1 factor decomposition and evidence counts), a 404 renders the
// honest generate card (never a fake run), an empty run renders honest quiet,
// and act/dismiss mark the item without removing it from the morning's view.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

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

const fleetDeal = {
  id: "d-1",
  workspace_id: "w",
  name: "Fleet retrofit",
  amount_minor: 4_800_000,
  currency: "EUR",
  pipeline_id: "pl",
  stage_id: "s2",
  status: "open",
  stalled: false,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-05-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const run = {
  id: "br-1",
  generated_at: "2026-07-05T05:30:00Z",
  as_of: "2026-07-05T05:00:00Z",
  candidate_count: 1,
  items: [
    {
      id: "bi-1",
      deal_id: "d-1",
      rank: 1,
      composite: 0.74,
      feature_vector: {
        winnability: 0.4,
        revenue: 1,
        timing: 0.75,
        momentum: 1,
        warmth: 0.47,
      },
      evidence_ids: ["ev-1", "ev-2"],
      state: "new",
      state_at: null,
    },
  ],
};

const emptyPage = { data: [], page: { next_cursor: null } };

// Routes the stubbed fetch by path+method so each test declares only the
// interesting responses; everything else answers an empty page.
function stubApi(
  routes: Record<string, (init?: RequestInit) => Response>,
): ReturnType<typeof vi.fn> {
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = new URL(
      request ? request.url : String(input),
      "https://test.local",
    );
    const method = request?.method ?? init?.method ?? "GET";
    const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
    const handler = routes[key];
    return handler ? handler(init) : jsonResponse(emptyPage);
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("HomeScreen (Morning Brief on the /brief spine)", () => {
  it("renders the ranked run: deal name, factor decomposition, evidence count, honest-short line", async () => {
    stubApi({
      "GET /brief": () => jsonResponse(run),
      "GET /deals/d-1": () => jsonResponse(fleetDeal),
    });
    render(<HomeScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    expect(screen.getByText("#1")).toBeTruthy();
    expect(screen.getByText("score 74%")).toBeTruthy();
    expect(screen.getByText("Winnability")).toBeTruthy();
    expect(screen.getByText("Warmth")).toBeTruthy();
    expect(screen.getByText("2 evidence rows")).toBeTruthy();
    expect(
      screen.getByText(
        "Only 1 deals cleared the bar — the queue is never padded.",
      ),
    ).toBeTruthy();
  });

  it("a 404 (no run yet) renders the generate card, and generating renders the fresh run", async () => {
    let generated = false;
    stubApi({
      "GET /brief": () =>
        generated
          ? jsonResponse(run)
          : jsonResponse({ title: "Not Found" }, 404),
      "POST /brief": () => {
        generated = true;
        return jsonResponse(run, 201);
      },
      "GET /deals/d-1": () => jsonResponse(fleetDeal),
    });
    render(<HomeScreen />);
    await waitFor(() => expect(screen.getByText("No brief yet")).toBeTruthy());
    await userEvent.click(screen.getByText("Generate my first brief"));
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
  });

  it("acting on an item marks it acted in place (still visible, receded)", async () => {
    stubApi({
      "GET /brief": () => jsonResponse(run),
      "GET /deals/d-1": () => jsonResponse(fleetDeal),
      "POST /brief/items/bi-1/act": () =>
        jsonResponse({
          ...run.items[0],
          state: "acted",
          state_at: "2026-07-05T06:00:00Z",
        }),
    });
    render(<HomeScreen />);
    await waitFor(() =>
      expect(screen.getByText("Fleet retrofit")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("Done"));
    await waitFor(() => expect(screen.getByText("acted")).toBeTruthy());
    expect(screen.getByText("Fleet retrofit")).toBeTruthy();
  });

  it("an empty run renders honest quiet — no invented urgency", async () => {
    stubApi({
      "GET /brief": () =>
        jsonResponse({ ...run, candidate_count: 0, items: [] }),
    });
    render(<HomeScreen />);
    await waitFor(() =>
      expect(
        screen.getByText(
          "Nothing cleared the bar this morning. No invented urgency — enjoy the quiet.",
        ),
      ).toBeTruthy(),
    );
  });

  // AC-4 cross-surface: approving a morning-brief row mints an approval_token
  // too. The row unmounts on the pending invalidation, so the token must be
  // caught at screen level (the shared useApprovalTokenSink) on Home as well —
  // not just InboxScreen.
  it("surfaces the minted token at screen level when approving a Home-rendered row, surviving the refetch", async () => {
    let approved = false;
    const staged = {
      id: "ap-h1",
      workspace_id: "w",
      kind: "send_email",
      status: "pending",
      proposed_by: "agent:runner",
      summary: "Send the Home follow-up",
      proposed_change: { subject: "Hi" },
      created_at: "2026-07-05T05:00:00Z",
    };
    stubApi({
      "GET /brief": () => jsonResponse({ title: "Not Found" }, 404),
      "GET /approvals": () => jsonResponse({ data: approved ? [] : [staged] }),
      "POST /approvals/ap-h1/approve": () => {
        approved = true;
        return jsonResponse({
          ...staged,
          status: "approved",
          approval_token: "example-home-token",
        });
      },
    });
    render(<HomeScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    // The approved row leaves the pending list on refetch…
    await waitFor(() => expect(screen.queryByText("send_email")).toBeNull());
    // …but the once-shown token stays visible + copyable at screen level.
    expect(screen.getByText("example-home-token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  });

  // AC-6 cross-surface: a 409 already_decided from a Home-rendered row must
  // show the honest screen-level note (same shared sink as InboxScreen), not
  // drop the row silently.
  it("shows the already-decided note at screen level when a Home approve 409s", async () => {
    let decidedElsewhere = false;
    const staged = {
      id: "ap-h2",
      workspace_id: "w",
      kind: "send_email",
      status: "pending",
      proposed_by: "agent:runner",
      summary: "Send the Home follow-up",
      proposed_change: { subject: "Hi" },
      created_at: "2026-07-05T05:00:00Z",
    };
    stubApi({
      "GET /brief": () => jsonResponse({ title: "Not Found" }, 404),
      "GET /approvals": () =>
        jsonResponse({ data: decidedElsewhere ? [] : [staged] }),
      "POST /approvals/ap-h2/approve": () => {
        decidedElsewhere = true;
        return jsonResponse(
          { title: "Conflict", code: "already_decided" },
          409,
        );
      },
    });
    render(<HomeScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    // Stale row leaves…
    await waitFor(() => expect(screen.queryByText("send_email")).toBeNull());
    // …and the honest note is surfaced at screen level (not a silent drop).
    expect(
      screen.getByText("Already decided — nothing left to do here."),
    ).toBeTruthy();
  });
});
