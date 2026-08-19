/** @vitest-environment jsdom */
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
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { InboxScreen } from "./inbox";

// The inbox reads one act as one question (D2/R7). The server stamps every
// proposal ONE act staged with a shared `bundle_id` — a site read publishes the
// company's facts plus a lead per person on the team page — and the queue used
// to render each of them as its own top-level card, so a ten-person team page
// asked eleven questions about one decision.
//
// What is asserted here: the grouping itself, that the group's own controls
// decide it in ONE call through the bundle routes, that a member stays
// individually decidable underneath (the routes have no edit arm, so a reader
// who wants to change one must be able to reach it), that the per-member
// outcomes are reported rather than flattened into "done", and that a bundle of
// one is a row rather than a group holding a single child.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Approval = components["schemas"]["Approval"];

const BUNDLE = "018f3a1b-0000-7000-8000-0000000000b1";

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

const facts: Approval = {
  id: "ap-facts",
  kind: "deepread",
  status: "pending",
  proposed_by: "agent:runner",
  bundle_id: BUNDLE,
  summary: "Deep site read of acme.example: 6 fields, 4 facts from 3 pages",
  proposed_change: { source_url: "https://acme.example" },
  created_at: "2026-07-05T05:00:00Z",
} as Approval;

function lead(id: string, name: string): Approval {
  return {
    ...facts,
    id,
    kind: "site_lead",
    summary: `Lead from acme.example: ${name} — Head of Operations`,
    proposed_change: { name },
  } as Approval;
}

const siteRead: Approval[] = [
  facts,
  lead("ap-lead-1", "Anna Weber"),
  lead("ap-lead-2", "Kilian Wenzel"),
  lead("ap-lead-3", "Mira Osei"),
];

// The subject the card leads with: what the act HOLDS, in the order it staged
// it. A count on its own ("4 proposals") is not something a reader can answer.
const SUBJECT = "Read the company site · Add a person found on the site";

type Call = { url: string; method: string; body: unknown };

async function bodyOf(
  request: Request | null,
  init?: RequestInit,
): Promise<unknown> {
  try {
    return request ? await request.json() : JSON.parse(String(init?.body));
  } catch {
    // A POST the screen sends with no body at all — the bundle routes take an
    // optional one — is a call with no body, not a failure to read it.
    return null;
  }
}

// The queue the reader is looking at, plus whatever the bundle routes answer.
function inboxBackend(
  calls: Call[],
  pending: Approval[],
  bundleResponse: () => Response = () =>
    jsonResponse({
      bundle_id: BUNDLE,
      data: pending.map((approval) => ({
        approval: { ...approval, status: "approved" },
        outcome: "decided",
      })),
    }),
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    if (url.includes("/agent-tools")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (method === "POST") {
      calls.push({ url, method, body: await bodyOf(request, init) });
      if (url.includes("/approval-bundles/")) {
        return bundleResponse();
      }
      return jsonResponse({ ...pending[0], status: "approved" });
    }
    if (/\/approvals(\?|$)/.test(url)) {
      const status = /[?&]status=([^&]+)/.exec(url)?.[1];
      return jsonResponse({
        data: status === "pending" ? pending : [],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

function bundleCard(): HTMLElement {
  return screen.getByRole("article", { name: SUBJECT });
}

describe("the inbox groups one act's proposals (issue #662)", () => {
  it("draws one card for the act and holds its members inside it", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls, siteRead));
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    // The defect was four cards where the act asked one question: every staged
    // row now sits INSIDE the group rather than beside it.
    const rows = document.querySelectorAll(".staging-card");
    expect(rows).toHaveLength(4);
    for (const row of rows) {
      expect(card.contains(row)).toBe(true);
    }
    // A list, not an indent: the group's size and boundaries have to reach a
    // reader who is hearing the page.
    expect(within(card).getAllByRole("listitem")).toHaveLength(4);
    expect(
      within(card).getByText("The 4 proposals", { selector: "span" }),
    ).toBeTruthy();
    // One act, one author — stated by the GROUP, not read off one member. Its
    // own header line is the assertion: every member row carries the same tag,
    // so a document-wide lookup would pass on a card that said nothing.
    expect(card.querySelector(".approval-bundle-meta")?.textContent).toContain(
      "Automated by runner",
    );
  });

  it("approves the whole act in ONE call to the bundle route", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls, siteRead));
    const user = userEvent.setup();
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    await user.click(
      within(card).getByRole("button", { name: "Approve all 4" }),
    );
    // Approving four effects at once is not a press to make by accident.
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Accept" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain(`/approval-bundles/${BUNDLE}/approve`);
    // Not four per-member decisions dressed up as one control.
    expect(
      calls.some((call) => /\/approvals\/[^/]+\/approve/.test(call.url)),
    ).toBe(false);
  });

  it("rejects the act with one stated reason, recorded on every member", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls, siteRead));
    const user = userEvent.setup();
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    await user.click(
      within(card).getByRole("button", { name: "Reject all 4" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.type(
      within(dialog).getByRole("textbox", { name: "Reason" }),
      "The site is a reseller, not the company.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Reject" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain(`/approval-bundles/${BUNDLE}/reject`);
    expect(calls[0].body).toEqual({
      reason: "The site is a reseller, not the company.",
    });
  });

  it("keeps every member decidable on its own", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls, siteRead));
    const user = userEvent.setup();
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    // The bundle routes carry no edit arm — an edit re-hashes ONE diff — so a
    // reader who disagrees with one proposal has to be able to reach it.
    const member = within(card)
      .getByText("Lead from acme.example: Anna Weber — Head of Operations")
      .closest(".staging-card");
    if (!(member instanceof HTMLElement)) {
      throw new Error("the member row is not rendered inside the group");
    }
    await user.click(within(member).getByRole("button", { name: "Accept" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toContain("/approvals/ap-lead-1/approve");
  });

  it("reports what each member did, not just that the call succeeded", async () => {
    const calls: Call[] = [];
    vi.stubGlobal(
      "fetch",
      inboxBackend(calls, siteRead, () =>
        jsonResponse({
          bundle_id: BUNDLE,
          data: [
            { approval: facts, outcome: "decided" },
            { approval: siteRead[1], outcome: "already_decided" },
            { approval: siteRead[2], outcome: "expired" },
            { approval: siteRead[3], outcome: "effect_failed" },
          ],
        }),
      ),
    );
    const user = userEvent.setup();
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    await user.click(
      within(card).getByRole("button", { name: "Approve all 4" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Accept" }));

    // Deciding a bundle is not all-or-nothing, so "approved" alone would claim
    // three things the response did not say.
    const report = await screen.findByRole("status");
    expect(within(report).getByText("1 approved")).toBeTruthy();
    expect(
      within(report).getByText(
        "1 already carried a verdict, which stands as it was.",
      ),
    ).toBeTruthy();
    expect(
      within(report).getByText(
        "1 had already lapsed — propose it again to decide it.",
      ),
    ).toBeTruthy();
    expect(
      within(report).getByText(
        "1 was approved, but its change did not land — that record is unchanged.",
      ),
    ).toBeTruthy();
  });

  it("reads a bundle of one as a plain row", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", inboxBackend(calls, [facts]));
    render(<InboxScreen />);

    // A group holding a single child hides the very question it exists to
    // present, and costs the reader a click for nothing.
    await waitFor(() =>
      expect(
        screen.getByText(
          "Deep site read of acme.example: 6 fields, 4 facts from 3 pages",
        ),
      ).toBeTruthy(),
    );
    expect(screen.queryByRole("article", { name: SUBJECT })).toBeNull();
    expect(document.querySelectorAll(".staging-card")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Accept" })).toBeTruthy();
  });

  it("says WHEN the act starts losing proposals, not that all of it lapses then", async () => {
    const soon = new Date(Date.now() + 90 * 60_000).toISOString();
    const later = new Date(Date.now() + 40 * 60 * 60_000).toISOString();
    const calls: Call[] = [];
    vi.stubGlobal(
      "fetch",
      inboxBackend(calls, [
        { ...facts, expires_at: later },
        { ...siteRead[1], expires_at: soon },
        { ...siteRead[2], expires_at: later },
        { ...siteRead[3], expires_at: later },
      ]),
    );
    render(<InboxScreen />);

    const card = await waitFor(bundleCard);
    // The soonest member's countdown, said as the FIRST one: members keep their
    // own expiries, and a bare countdown in a group header reads as one deadline
    // shared by all of them.
    expect(within(card).getByText(/^first expires in 1h/)).toBeTruthy();
  });
});
