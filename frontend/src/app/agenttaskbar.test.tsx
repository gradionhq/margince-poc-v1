/** @vitest-environment jsdom */

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { AgentTaskbar } from "./agenttaskbar";
import { LABELS, REVIEW_ONLY, VOCABULARY } from "./agenttaskbar-copy";
import { type GrantSpec, meFixture } from "./mefixture";
import type { Route } from "./router";

// The bottom taskbar preview reads every one of its right-half facts off the
// wire (approvals, connectors, dedupe, the served model, the tool catalog, the
// account's own suggestions) — nothing left standing in for a read that has
// not answered. Every case here proves the bar draws exactly what the API
// said, and never a number nobody computed.

type Connector = components["schemas"]["CaptureConnection"];
type Candidate = components["schemas"]["DedupeCandidate"];
type Approval = components["schemas"]["Approval"];
type AiCallSummary = components["schemas"]["AiCallSummary"];
type ToolCatalog = components["schemas"]["AgentToolListResponse"];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const emptyPage = { has_more: false, next_cursor: null };

const CONNECTED: Connector = {
  id: "018f3a1b-0000-7000-8000-0000000000c1",
  provider: "gmail",
  status: "connected",
  scopes: ["https://www.googleapis.com/auth/gmail.readonly"],
};

const CANDIDATE = (id: string): Candidate => ({
  id,
  entity_type: "organization",
  left_id: "o-1",
  right_id: "o-2",
  confidence: 0.91,
  evidence: [],
  status: "open",
  created_at: "2026-08-01T09:00:00Z",
});

const APPROVAL = (id: string): Approval => ({
  id,
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  created_at: "2026-08-01T09:00:00Z",
});

const AI_CALL: AiCallSummary = {
  id: "019f7e65-fbf7-7114-b114-40af4af63ae8",
  occurred_at: "2026-07-20T10:00:00Z",
  task: "capture_classify",
  tier: "cheap_cloud",
  provider: "gemini",
  model_id: "configured",
  served_model: "served",
  calls_attempted: 1,
  tokens_in: 10,
  tokens_out: 5,
  reasoning_tokens: 0,
  cached_tokens: 0,
  latency_ms: 400,
  cache_hit: false,
  degraded: false,
  has_payload: false,
};

const CATALOG: ToolCatalog = {
  data: [
    {
      name: "enrich",
      title: "enrich",
      description: "what enrich does",
      tier: "auto_execute",
      egress: false,
    },
  ],
};

const OPERATOR: GrantSpec = { automation: ["update"] };

const ORG_360_VIEW = {
  as_of: "2026-08-01T09:00:00Z",
  organization: {
    id: "o-1",
    display_name: "Brandt Automotive GmbH",
    captured_by: "human:u1",
    source: "manual",
    version: 1,
    created_at: "2026-06-01T08:00:00Z",
    updated_at: "2026-06-01T08:00:00Z",
  },
  sections_omitted: [],
  people: { data: [], page: emptyPage },
  deals: {
    data: [],
    page: emptyPage,
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    lost_count: 0,
  },
  activities: { data: [], page: emptyPage },
  next_steps: { data: [], page: emptyPage },
  pending_approvals: { data: [], page: emptyPage },
  tags: [],
  list_memberships: [],
  since_last_visit: {
    baseline_at: "2026-05-30T09:00:00Z",
    new_activities: 0,
    deal_stage_moves: 0,
    pending_proposals: 0,
  },
};

type FetchRoutes = {
  connectors?: () => Response | Promise<Response>;
  dedupe?: () => Response | Promise<Response>;
  approvals?: () => Response | Promise<Response>;
  aiCalls?: () => Response | Promise<Response>;
  agentTools?: () => Response | Promise<Response>;
  me?: () => Response | Promise<Response>;
};

// Routes every hook the bar depends on by pathname, the way company360.test.tsx
// does for its own composite read. The healthy default answers every source as
// connected, every queue empty and the seat allowed to read the runtime — each
// test overrides only the one route its case is about.
function stubTaskbarApi(routes: FetchRoutes = {}) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      if (pathname.endsWith("/connectors")) {
        return routes.connectors
          ? routes.connectors()
          : jsonResponse({ data: [CONNECTED] });
      }
      if (pathname.endsWith("/dedupe/candidates")) {
        return routes.dedupe
          ? routes.dedupe()
          : jsonResponse({ data: [], page: emptyPage });
      }
      if (pathname.endsWith("/approvals")) {
        return routes.approvals
          ? routes.approvals()
          : jsonResponse({ data: [], page: emptyPage });
      }
      if (pathname.endsWith("/ai/calls")) {
        return routes.aiCalls
          ? routes.aiCalls()
          : jsonResponse({
              data: [],
              page: emptyPage,
              payload_capture_enabled: false,
              tasks: [],
            });
      }
      if (pathname.endsWith("/agent-tools")) {
        return routes.agentTools ? routes.agentTools() : jsonResponse(CATALOG);
      }
      if (pathname.endsWith("/me")) {
        return routes.me
          ? routes.me()
          : jsonResponse(meFixture({ allow: OPERATOR }));
      }
      if (pathname.endsWith("/360")) {
        return jsonResponse({ state: "ready", view: ORG_360_VIEW });
      }
      return jsonResponse({ data: [], page: emptyPage });
    }),
  );
}

function render(route: Route) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <AgentTaskbar route={route} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

const ROUTE: Route = { screen: "deals" };

const dock = (container: HTMLElement) => {
  const el = container.querySelector(".tbdock");
  if (!el) throw new Error("no .tbdock in the rendered tree");
  return el;
};

const openPanel = async () => {
  const trigger = document.querySelector(".tbhit");
  if (!trigger) throw new Error("no .tbhit trigger in the rendered tree");
  await userEvent.click(trigger);
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AgentTaskbar", () => {
  it("is dormant with the idle line when every source is connected and nothing is queued", async () => {
    stubTaskbarApi();
    const { container } = render(ROUTE);
    await waitFor(() =>
      expect(dock(container).getAttribute("data-core-state")).toBe("dormant"),
    );
    expect(container.querySelector(".tbline")?.textContent).toBe(LABELS.idle);
  });

  it("goes disconnected and names the source when a connector cannot reach it", async () => {
    stubTaskbarApi({
      connectors: () =>
        jsonResponse({
          data: [
            {
              ...CONNECTED,
              status: "reauth_required",
              account_label: "sales@old-crm.example",
            },
          ],
        }),
    });
    const { container } = render(ROUTE);
    await waitFor(() =>
      expect(dock(container).getAttribute("data-core-state")).toBe(
        "disconnected",
      ),
    );
    expect(container.querySelector(".tbline")?.textContent).toContain(
      "sales@old-crm.example",
    );
    expect(
      screen.getByRole("link", { name: LABELS.reconnect }).getAttribute("href"),
    ).toBe("#/settings/connections");
  });

  it("goes flagged when every source is connected but duplicates are open", async () => {
    stubTaskbarApi({
      dedupe: () =>
        jsonResponse({ data: [CANDIDATE("d-1"), CANDIDATE("d-2")] }),
    });
    const { container } = render(ROUTE);
    await waitFor(() =>
      expect(dock(container).getAttribute("data-core-state")).toBe("flagged"),
    );
    expect(
      screen.getByRole("link", { name: LABELS.decide }).getAttribute("href"),
    ).toBe("#/dedupe");
  });

  it("shows the waiting count and sends the reviewer to the inbox", async () => {
    stubTaskbarApi({
      approvals: () =>
        jsonResponse({
          data: [APPROVAL("a-1"), APPROVAL("a-2"), APPROVAL("a-3")],
          page: emptyPage,
        }),
    });
    const { container } = render(ROUTE);
    await waitFor(() =>
      expect(container.querySelector(".tbline")?.textContent).toBe(
        `3 ${LABELS.waiting}`,
      ),
    );
    expect(
      screen.getByRole("link", { name: LABELS.review }).getAttribute("href"),
    ).toBe("#/inbox");
  });

  // Absence, not zero: a count nobody has computed yet must not be printed —
  // neither as the bar's own badge nor as a row in the panel. Asserted before
  // the approvals fetch ever settles, which is the state a status surface has
  // to be honest in.
  it("prints no count and no approvals row while the approvals read has not answered", async () => {
    stubTaskbarApi({ approvals: () => new Promise<Response>(() => {}) });
    const { container } = render(ROUTE);
    await openPanel();
    expect(container.querySelector(".tbbadge")).toBeNull();
    expect(
      screen.queryByRole("link", { name: new RegExp(`^${LABELS.approvals}`) }),
    ).toBeNull();
  });

  // A refusal reads exactly like a read that has not answered — the row must
  // stay absent once the 403 lands too, not just before it.
  it("prints no count and no approvals row when the approvals read is refused", async () => {
    stubTaskbarApi({
      approvals: () =>
        jsonResponse(
          { code: "permission_denied", title: "denied", status: 403 },
          403,
        ),
    });
    const { container } = render(ROUTE);
    await openPanel();
    await waitFor(() =>
      expect(
        screen.queryByRole("link", {
          name: new RegExp(`^${LABELS.approvals}`),
        }),
      ).toBeNull(),
    );
    expect(container.querySelector(".tbbadge")).toBeNull();
  });

  it("opens the panel on a click of the bar and flips its expanded state", async () => {
    stubTaskbarApi();
    const { container } = render(ROUTE);
    expect(container.querySelector(".tbpanel")).toBeNull();
    const trigger = container.querySelector(".tbhit");
    expect(trigger?.getAttribute("aria-expanded")).toBe("false");

    await openPanel();
    expect(container.querySelector(".tbpanel")).not.toBeNull();
    expect(
      container.querySelector(".tbhit")?.getAttribute("aria-expanded"),
    ).toBe("true");
  });

  it("lets a review-only chip override the derived state with its invented line", async () => {
    stubTaskbarApi();
    const { container } = render(ROUTE);
    await openPanel();

    await userEvent.click(screen.getByRole("button", { name: "reasoning" }));
    expect(dock(container).getAttribute("data-core-state")).toBe("reasoning");
    expect(container.querySelector(".tbline")?.textContent).toBe(
      REVIEW_ONLY.reasoning,
    );
  });

  it("renders nothing on the full Ask surface and on a railless screen", () => {
    stubTaskbarApi();
    const ai = render({ screen: "ai" });
    expect(ai.container.querySelector(".tbdock")).toBeNull();
    cleanup();

    const onboarding = render({ screen: "onboarding" });
    expect(onboarding.container.querySelector(".tbdock")).toBeNull();
  });

  it("says the runtime row is not readable on a seat without automation:update", async () => {
    stubTaskbarApi({ me: () => jsonResponse(meFixture({ allow: {} })) });
    const { container } = render(ROUTE);
    await openPanel();
    await waitFor(() =>
      expect(container.querySelector(".tbmeta")?.textContent).toContain(
        LABELS.unreadable,
      ),
    );
  });

  it("says nothing has run yet when the seat may read /ai/calls and none has", async () => {
    stubTaskbarApi();
    const { container } = render(ROUTE);
    await openPanel();
    await waitFor(() =>
      expect(container.querySelector(".tbmeta")?.textContent).toContain(
        LABELS.noCallsYet,
      ),
    );
  });

  it("names the served model once a call has actually run", async () => {
    stubTaskbarApi({
      aiCalls: () =>
        jsonResponse({
          data: [AI_CALL],
          page: emptyPage,
          payload_capture_enabled: false,
          tasks: [AI_CALL.task],
        }),
    });
    const { container } = render(ROUTE);
    await openPanel();
    await waitFor(() =>
      expect(container.querySelector(".tbmeta")?.textContent).toContain(
        `${AI_CALL.provider}/${AI_CALL.served_model}`,
      ),
    );
  });

  // The Core's own tone rule is the only place a state's colour is declared
  // (agenttaskbar.css comment above `.tbdock`), so a state added to the
  // vocabulary with no rule here would silently draw the accent default
  // instead of its own tone. Only "dormant" is genuinely undocumented — it is
  // the bar's resting state and carries no attribute rule of its own; every
  // other member (including "applied", which shares ingesting's jade rule)
  // has one.
  it("carries a CSS tone rule for every state in the vocabulary but the resting default", () => {
    const cssPath = join(
      dirname(fileURLToPath(import.meta.url)),
      "agenttaskbar.css",
    );
    const css = readFileSync(cssPath, "utf8");
    const documentedDefaults = new Set(["dormant"]);
    for (const state of VOCABULARY) {
      if (documentedDefaults.has(state)) continue;
      expect(css).toContain(`data-core-state="${state}"`);
    }
  });
});
