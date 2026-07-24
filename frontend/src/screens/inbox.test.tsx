/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { HomeScreen } from "./home";
import { confidenceLevel, InboxScreen } from "./inbox";
import { groupTask, TasksScreen } from "./tasks";

// B-EP09.12a/b/d + Task 10 (AC-1..7) acceptance: approve/reject/edit from the
// pending rows, the Decided view (approved + rejected from their own status
// queries, EXPIRED salvaged from the status=pending response because the
// contract exposes no status=expired filter — the server expires lazily and
// wires status="expired" back), detail modal, reject-with-reason, the
// once-shown approval token, honest version-skew / already-decided branches,
// and the live expiry countdown → Expired badge.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.location.hash = "";
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
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

type Approval = components["schemas"]["Approval"];

const approval: Approval = {
  id: "ap-1",
  workspace_id: "w",
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  summary: "Send the follow-up to Anna Weber",
  proposed_change: {
    subject: "Follow-up",
    body: "Hi Anna — shall we sync next week?",
  },
  confidence: 0.62,
  evidence: [
    { evidence_snippet: "…shall we sync next week?…", source_type: "activity" },
  ],
  target_version: 3,
  on_behalf_of: "u-99",
  created_at: "2026-07-05T05:00:00Z",
} as Approval;

// Reads the status filter off a listApprovals URL (null for the by-id GET).
function statusOf(url: string): string | null {
  const match = /[?&]status=([^&]+)/.exec(url);
  return match ? match[1] : null;
}

function isListUrl(url: string): boolean {
  return /\/approvals(\?|$)/.test(url);
}

function isDetailUrl(url: string): boolean {
  return (
    /\/approvals\/[^/?]+(\?|$)/.test(url) && !/\/(approve|reject)/.test(url)
  );
}

function inboxBackend(
  calls: { url: string; body: unknown }[],
  agentTools: components["schemas"]["AgentTool"][] = [],
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/agent-tools")) {
      return jsonResponse({ data: agentTools, page: { next_cursor: null } });
    }
    if (method === "POST" && /approvals\/ap-1\/(approve|reject)/.test(url)) {
      let body: unknown = null;
      try {
        body = request ? await request.json() : JSON.parse(String(init?.body));
      } catch {
        body = null;
      }
      calls.push({ url, body });
      return jsonResponse({ ...approval, status: "approved" });
    }
    if (url.includes("/digest")) {
      // no nightly digest yet — home renders no digest card at all
      return jsonResponse({ title: "Not Found", code: "no_digest_yet" }, 404);
    }
    if (url.includes("/brief")) {
      // no run persisted yet — home renders the honest generate card
      return jsonResponse({ title: "Not Found" }, 404);
    }
    if (isListUrl(url)) {
      const status = statusOf(url);
      calls.push({ url, body: null });
      if (status === "pending") {
        return jsonResponse({ data: [approval], page: { next_cursor: null } });
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

describe("InboxScreen (B-EP09.12a)", () => {
  it("approves a queued item", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    expect(screen.getByText("agent: runner")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("/approve"))).toBe(true),
    );
  });

  it("the inline editor approves with edited_payload (edit-then-send)", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const subject = screen.getByRole("textbox", { name: "subject" });
    await userEvent.clear(subject);
    await userEvent.type(subject, "Follow-up (edited)");
    await userEvent.click(
      screen.getByRole("button", { name: "Approve edited" }),
    );
    const posts = () => calls.filter((c) => c.url.includes("/approve"));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toMatchObject({
      edited_payload: { subject: "Follow-up (edited)" },
    });
  });

  it("the row dot reads the live catalog tier, not a hardcode", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      inboxBackend(calls, [
        {
          name: "send_email",
          required_scope: "write",
          tier: "auto_execute",
          egress: true,
        },
      ]),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    // send_email is catalogued "auto_execute" — a hardcoded
    // "confirm" dot would render "confirm-first" here instead.
    await waitFor(() =>
      expect(screen.getByLabelText("auto-execute")).toBeTruthy(),
    );
  });

  it("shows the originating tool verb next to the kind", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      inboxBackend(calls, [
        {
          name: "send_email",
          required_scope: "write",
          tier: "confirmation_required",
          egress: true,
        },
      ]),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await waitFor(() =>
      expect(screen.getByText("via send_email")).toBeTruthy(),
    );
  });
});

// ── AC-3: reject-with-reason ────────────────────────────────────────────
describe("InboxScreen — reject with reason (AC-3)", () => {
  it("opens a reason field and sends the reason in the reject body", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls));
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    // The row's Reject opens the confirm modal (no POST yet).
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Reason"), "Not now");
    expect(calls.some((c) => c.url.includes("/reject"))).toBe(false);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Reject" }),
    );
    const posts = () => calls.filter((c) => c.url.includes("/reject"));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0].body).toMatchObject({ reason: "Not now" });
  });
});

// ── AC-1: Decided view (read-only merge, expired salvaged from pending) ──
describe("InboxScreen — Decided view (AC-1)", () => {
  const pendingRow = {
    ...approval,
    id: "ap-pending",
    kind: "pending_kind",
    summary: "Still pending action",
    status: "pending",
  } as Approval;
  const expiredRow = {
    ...approval,
    id: "ap-expired",
    kind: "expired_kind",
    summary: "Lapsed action",
    // lazily expired: DB row is pending, but the server wires status="expired"
    status: "expired",
    expires_at: "2026-07-01T00:00:00Z",
  } as Approval;
  const approvedRow = {
    ...approval,
    id: "ap-approved",
    kind: "approved_kind",
    summary: "Committed action",
    status: "approved",
    decided_at: "2026-07-06T09:00:00Z",
  } as Approval;
  const rejectedRow = {
    ...approval,
    id: "ap-rejected",
    kind: "rejected_kind",
    summary: "Declined action",
    status: "rejected",
    decided_at: "2026-07-06T10:00:00Z",
  } as Approval;

  function partitionBackend(urls: string[]) {
    return vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (isListUrl(url)) {
        urls.push(url);
        const status = statusOf(url);
        if (status === "pending") {
          return jsonResponse({ data: [pendingRow, expiredRow] });
        }
        if (status === "approved") {
          return jsonResponse({ data: [approvedRow] });
        }
        if (status === "rejected") {
          return jsonResponse({ data: [rejectedRow] });
        }
      }
      return jsonResponse({ data: [] });
    });
  }

  it("Pending tab excludes lazily-expired rows and shows actionable ones", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", partitionBackend(urls));
    render(<InboxScreen />);
    await waitFor(() =>
      expect(screen.getByText("Still pending action")).toBeTruthy(),
    );
    // The expired row must NOT appear as actionable in Pending.
    expect(screen.queryByText("Lapsed action")).toBeNull();
    expect(screen.getByRole("button", { name: "Accept" })).toBeTruthy();
  });

  it("Decided tab queries approved/rejected/pending and buckets expired in, still-pending out, read-only", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", partitionBackend(urls));
    render(<InboxScreen />);
    await waitFor(() =>
      expect(screen.getByText("Still pending action")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Decided" }));

    // It queries all three status filters (expired is NOT a filter).
    await waitFor(() =>
      expect(urls.some((u) => statusOf(u) === "approved")).toBe(true),
    );
    expect(urls.some((u) => statusOf(u) === "rejected")).toBe(true);
    expect(urls.some((u) => statusOf(u) === "pending")).toBe(true);
    expect(urls.some((u) => statusOf(u) === "expired")).toBe(false);

    // Approved + rejected + the salvaged expired row all render.
    await waitFor(() =>
      expect(screen.getByText("Committed action")).toBeTruthy(),
    );
    expect(screen.getByText("Declined action")).toBeTruthy();
    expect(screen.getByText("Lapsed action")).toBeTruthy();
    // The still-pending row does NOT leak into Decided.
    expect(screen.queryByText("Still pending action")).toBeNull();
    // Status badges present; read-only — no decision buttons.
    expect(screen.getByText("Approved")).toBeTruthy();
    expect(screen.getByText("Rejected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
  });

  it("orders decided rows by decided_at desc, expired falling back to expires_at", async () => {
    const urls: string[] = [];
    vi.stubGlobal("fetch", partitionBackend(urls));
    const { container } = render(<InboxScreen />);
    await waitFor(() =>
      expect(screen.getByText("Still pending action")).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Decided" }));
    await waitFor(() =>
      expect(screen.getByText("Committed action")).toBeTruthy(),
    );
    // rejected decided 07-06 10:00 > approved 07-06 09:00 > expired
    // (no decided_at → expires_at fallback 07-01) — newest decision first.
    const order = Array.from(container.querySelectorAll("[data-approval]")).map(
      (el) => el.getAttribute("data-approval"),
    );
    expect(order).toEqual(["ap-rejected", "ap-approved", "ap-expired"]);
  });
});

// ── AC-2: detail modal ──────────────────────────────────────────────────
describe("InboxScreen — detail modal (AC-2)", () => {
  it("opens a modal that GETs /approvals/{id} and shows the full change + evidence + target_version", async () => {
    const detailFetches: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (isDetailUrl(url)) {
          detailFetches.push(url);
          return jsonResponse(approval);
        }
        if (isListUrl(url)) {
          const status = statusOf(url);
          return jsonResponse({
            data: status === "pending" ? [approval] : [],
          });
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(
      screen.getByRole("button", { name: "Approval detail" }),
    );
    await waitFor(() => expect(detailFetches.length).toBeGreaterThan(0));
    expect(detailFetches[0]).toContain("/approvals/ap-1");
    const dialog = screen.getByRole("dialog");
    // Full proposed_change (both keys), evidence, and target_version.
    expect(within(dialog).getByText("subject")).toBeTruthy();
    expect(within(dialog).getByText("body")).toBeTruthy();
    expect(within(dialog).getByText("target_version")).toBeTruthy();
    expect(within(dialog).getByText("3")).toBeTruthy();
    // Evidence rendered (chip text is split across nodes; assert via the chip).
    expect(dialog.querySelector(".evidence-chip")).toBeTruthy();
    expect(dialog.textContent).toContain("shall we sync next week?");
  });
});

// ── AC-4: approval token shown once ─────────────────────────────────────
describe("InboxScreen — approval token (AC-4)", () => {
  it("keeps the token copyable AFTER the approved row is dropped by the refetch", async () => {
    // Simulate the REAL lifecycle: once approved, the pending refetch no longer
    // returns the row, so it unmounts. A row-local token would vanish with it;
    // the screen-level surface must survive.
    let approved = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const url = String(request ? request.url : input);
        const method = request ? request.method : (init?.method ?? "GET");
        if (method === "POST" && /\/approve/.test(url)) {
          approved = true;
          return jsonResponse({
            ...approval,
            status: "approved",
            approval_token: "example-approval-token",
          });
        }
        if (isListUrl(url)) {
          const status = statusOf(url);
          return jsonResponse({
            data: status === "pending" && !approved ? [approval] : [],
          });
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    // The approved row leaves the pending list…
    await waitFor(() => expect(screen.queryByText("send_email")).toBeNull());
    // …but the once-shown token + Copy stay visible at screen level.
    expect(screen.getByText("example-approval-token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  });
});

// ── AC-5: version-skew re-stage state ───────────────────────────────────
describe("InboxScreen — version skew (AC-5)", () => {
  it("renders the re-stage state with a re-read CTA, distinct from a generic error", async () => {
    const listCalls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const url = String(request ? request.url : input);
        const method = request ? request.method : (init?.method ?? "GET");
        if (method === "POST" && /\/approve/.test(url)) {
          return jsonResponse(
            {
              title: "Conflict",
              detail: "if-match version 3 does not match current 4",
              code: "version_skew",
            },
            409,
          );
        }
        if (isListUrl(url)) {
          listCalls.push(url);
          const status = statusOf(url);
          return jsonResponse({
            data: status === "pending" ? [approval] : [],
          });
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() =>
      expect(
        screen.getByText(
          "This record changed since it was staged — re-stage it before deciding.",
        ),
      ).toBeTruthy(),
    );
    // Honest copy, NOT the raw server detail.
    expect(
      screen.queryByText("if-match version 3 does not match current 4"),
    ).toBeNull();
    const pendingBefore = listCalls.filter(
      (u) => statusOf(u) === "pending",
    ).length;
    await userEvent.click(screen.getByRole("button", { name: "Re-read" }));
    // The re-read CTA invalidates → refetches pending.
    await waitFor(() =>
      expect(
        listCalls.filter((u) => statusOf(u) === "pending").length,
      ).toBeGreaterThan(pendingBefore),
    );
  });
});

// ── AC-6: already-decided ───────────────────────────────────────────────
describe("InboxScreen — already decided (AC-6)", () => {
  it("drops the stale row but the screen-level note survives the refetch", async () => {
    // Same lifecycle as AC-4: the 409 invalidates pending, the row unmounts;
    // the note must persist at screen level so the human still SEES it.
    let decidedElsewhere = false;
    const listCalls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const url = String(request ? request.url : input);
        const method = request ? request.method : (init?.method ?? "GET");
        if (method === "POST" && /\/approve/.test(url)) {
          decidedElsewhere = true;
          return jsonResponse(
            { title: "Conflict", code: "already_decided" },
            409,
          );
        }
        if (isListUrl(url)) {
          listCalls.push(url);
          const status = statusOf(url);
          return jsonResponse({
            data: status === "pending" && !decidedElsewhere ? [approval] : [],
          });
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<InboxScreen />);
    await waitFor(() => expect(screen.getByText("send_email")).toBeTruthy());
    const pendingBefore = listCalls.filter(
      (u) => statusOf(u) === "pending",
    ).length;
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    // Stale row leaves…
    await waitFor(() => expect(screen.queryByText("send_email")).toBeNull());
    // …the note remains, and it is NOT the version-skew branch.
    expect(
      screen.getByText("Already decided — nothing left to do here."),
    ).toBeTruthy();
    expect(
      screen.queryByText(
        "This record changed since it was staged — re-stage it before deciding.",
      ),
    ).toBeNull();
    // The pending list was refetched (the invalidation fired).
    expect(
      listCalls.filter((u) => statusOf(u) === "pending").length,
    ).toBeGreaterThan(pendingBefore);
  });
});

// ── CodeRabbit [10]: pagination merges beyond the 50-item page cap ───────
describe("InboxScreen — approval list pagination", () => {
  it("follows next_cursor and merges a 2nd page instead of truncating at page 1", async () => {
    const pageOneRow = {
      ...approval,
      id: "ap-page1",
      summary: "Page one item",
    };
    const pageTwoRow = {
      ...approval,
      id: "ap-page2",
      summary: "Page two item",
    };
    const cursorsSeen: (string | null)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (isListUrl(url)) {
          const status = statusOf(url);
          if (status !== "pending") {
            return jsonResponse({ data: [], page: { next_cursor: null } });
          }
          const cursorMatch = /[?&]cursor=([^&]+)/.exec(url);
          const cursor = cursorMatch ? cursorMatch[1] : null;
          cursorsSeen.push(cursor);
          if (!cursor) {
            // Page 1: report more is available.
            return jsonResponse({
              data: [pageOneRow],
              page: { next_cursor: "c1", has_more: true },
            });
          }
          // Page 2 (fetched with the cursor page 1 handed back): the last page.
          return jsonResponse({
            data: [pageTwoRow],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }),
    );

    render(<InboxScreen />);

    await waitFor(() => expect(screen.getByText("Page one item")).toBeTruthy());
    // The 2nd page must have been requested (with page 1's cursor) and its
    // row merged into the same rendered list — not dropped at the 50-cap.
    await waitFor(() => expect(screen.getByText("Page two item")).toBeTruthy());
    expect(cursorsSeen).toEqual([null, "c1"]);
  });
});

// ── AC-7: live expiry countdown → Expired badge ─────────────────────────
describe("InboxScreen — live expiry countdown (AC-7)", () => {
  it("shows a live countdown that flips to Expired once expires_at passes", () => {
    const base = Date.UTC(2026, 6, 5, 12, 0, 0);
    vi.useFakeTimers();
    vi.setSystemTime(base);
    const row = {
      ...approval,
      status: "pending",
      expires_at: new Date(base + 60_000).toISOString(),
    } as Approval;
    // Seed the three status queries so no async fetch is needed under fake
    // timers — the row renders synchronously and useNow's interval is fake.
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
      },
    });
    client.setQueryData(["approvals", "pending"], { data: [row] });
    client.setQueryData(["approvals", "approved"], { data: [] });
    client.setQueryData(["approvals", "rejected"], { data: [] });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ data: [] })),
    );

    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <InboxScreen />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    // Live countdown chip while time remains, decision controls still live.
    expect(screen.getByText("expires in 1m 0s")).toBeTruthy();
    expect(screen.queryByText("Expired")).toBeNull();
    expect(screen.getByRole("button", { name: "Accept" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reject" })).toBeTruthy();

    // Cross the expiry; the useNow interval re-renders with the new clock.
    act(() => {
      vi.advanceTimersByTime(61_000);
    });

    expect(screen.getByText("Expired")).toBeTruthy();
    expect(screen.queryByText("expires in 1m 0s")).toBeNull();
    // CodeRabbit [12]: Accept/Edit/Reject must disappear the moment
    // client-side expiry is detected — not linger until a refetch flips
    // approval.status server-side.
    expect(screen.queryByRole("button", { name: "Accept" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reject" })).toBeNull();
  });
});

describe("HomeScreen (B-EP09.12b)", () => {
  it("ranks staged approvals first, reusing the inbox rows", async () => {
    vi.stubGlobal("fetch", inboxBackend([]));
    render(<HomeScreen />);
    await waitFor(() =>
      expect(screen.getByText("Waiting on you")).toBeTruthy(),
    );
    expect(screen.getByText("send_email")).toBeTruthy();
    expect(screen.getByText("Nothing sent yet")).toBeTruthy();
  });

  it("renders the honest generate card when no brief run exists yet", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes("/digest")) {
          return jsonResponse(
            { title: "Not Found", code: "no_digest_yet" },
            404,
          );
        }
        if (url.includes("/brief")) {
          return jsonResponse({ title: "Not Found" }, 404);
        }
        return jsonResponse({ data: [], page: { next_cursor: null } });
      }),
    );
    render(<HomeScreen />);
    await waitFor(() => expect(screen.getByText("No brief yet")).toBeTruthy());
    expect(screen.getByText("Generate my first brief")).toBeTruthy();
  });
});

describe("TasksScreen (B-EP09.12d)", () => {
  const now = new Date("2026-07-05T12:00:00Z");

  it("groups by due date honestly", () => {
    const base = {
      id: "t",
      workspace_id: "w",
      kind: "task" as const,
      occurred_at: "2026-07-01T00:00:00Z",
      is_done: false,
      source: "manual",
      captured_by: "human:u",
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
    };
    expect(groupTask({ ...base, due_at: "2026-07-04T10:00:00Z" }, now)).toBe(
      "overdue",
    );
    expect(groupTask({ ...base, due_at: "2026-07-05T18:00:00Z" }, now)).toBe(
      "today",
    );
    expect(groupTask({ ...base, due_at: "2026-07-09T10:00:00Z" }, now)).toBe(
      "upcoming",
    );
    expect(groupTask({ ...base, due_at: null }, now)).toBe("undated");
  });

  it("completing a task PATCHes is_done", async () => {
    const patches: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = input instanceof Request ? input : null;
        const method = request ? request.method : (init?.method ?? "GET");
        if (method === "PATCH") {
          patches.push(
            request ? await request.json() : JSON.parse(String(init?.body)),
          );
          return jsonResponse({});
        }
        return jsonResponse({
          data: [
            {
              id: "t1",
              workspace_id: "w",
              kind: "task",
              subject: "Call Anna",
              occurred_at: "2026-07-01T00:00:00Z",
              due_at: "2026-07-04T10:00:00Z",
              is_done: false,
              source: "manual",
              captured_by: "human:u",
            },
          ],
          page: { next_cursor: null },
        });
      }),
    );
    render(<TasksScreen />);
    await waitFor(() => expect(screen.getByText("Call Anna")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(patches).toHaveLength(1));
    expect(patches[0]).toMatchObject({ is_done: true });
  });
});

describe("confidenceLevel mapping", () => {
  it("maps numeric confidence to the three-glyph vocabulary", () => {
    expect(confidenceLevel(0.9)).toBe("high");
    expect(confidenceLevel(0.6)).toBe("med");
    expect(confidenceLevel(0.2)).toBe("low");
    expect(confidenceLevel(null)).toBeNull();
  });
});
