// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GrantSpec } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { BlockedDomainsCard } from "./blocked-domains";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
} from "./story-utils";

// Settings → Capture: the domains this installation refuses a company. The card
// answers one question nothing else can — a company that never appeared, was it
// refused and by whom — so what it must never do is lose the difference between
// a machine's refusal and a person's, claim an empty list is a broken one, or
// offer a write it has no reason to send.

const LIST = "GET /capture/blocked-domains";
const WRITE = "PUT /capture/blocked-domains";

// Read is every human role's; changing an entry is organization:update.
const OPS: GrantSpec = { organization: ["read", "update"] };
const READER: GrantSpec = { organization: ["read"] };

const BY_HEURISTIC = {
  domain: "expensify.example",
  admission: "suppressed",
  reason: "bulk sender: no reply address",
  source: "heuristic",
  decided_at: "2026-08-02T14:40:00Z",
  organization_id: null,
};
const BY_HUMAN = {
  domain: "mckinsey.example",
  admission: "admitted",
  reason: "they became a client in July",
  source: "human",
  decided_at: "2026-08-11T07:05:00Z",
  organization_id: "018f3a1b-0000-7000-8000-00000000c001",
};

function mount(allow: GrantSpec, routes: RouteMap) {
  installFetchStub({ "GET /me": meRoute(allow), ...routes });
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <BlockedDomainsCard />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

/** The row a domain is on, so an assertion is about that domain's own cells. */
function rowFor(domain: string): HTMLElement {
  const cell = screen.getByText(domain);
  const row = cell.closest("tr");
  if (row === null) {
    throw new Error(`${domain} is not in a table row`);
  }
  return row;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("BlockedDomainsCard", () => {
  it("says of every decision whether a machine or a person made it", async () => {
    mount(OPS, {
      [LIST]: () => jsonResponse({ data: [BY_HEURISTIC, BY_HUMAN], total: 2 }),
    });

    await screen.findByText("expensify.example");
    // The distinction the card exists for: the same outcome, two different
    // facts. A row that named only the admission would leave an operator
    // unable to tell a bulk-sender verdict from somebody's deliberate call.
    expect(
      within(rowFor("expensify.example")).getByText(
        en["blockedDomains.source.heuristic"],
      ),
    ).toBeInTheDocument();
    expect(
      within(rowFor("mckinsey.example")).getByText(
        en["blockedDomains.source.human"],
      ),
    ).toBeInTheDocument();
    // And the reason, which is why the server requires one.
    expect(
      within(rowFor("expensify.example")).getByText(
        "bulk sender: no reply address",
      ),
    ).toBeInTheDocument();
  });

  it("reads as nothing-refused rather than as a table that failed to draw", async () => {
    mount(OPS, { [LIST]: () => jsonResponse({ data: [], total: 0 }) });

    expect(await screen.findByText(en["blockedDomains.none"])).toBeVisible();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("says how many decisions exist, not only how many this page carries", async () => {
    // Refusals accumulate on their own from every bulk-sender verdict, so the
    // server pages the list. An operator hunting a company that never appeared
    // must be able to tell "not refused" from "past the end of this page".
    mount(OPS, {
      [LIST]: () =>
        jsonResponse({ data: [BY_HEURISTIC, BY_HUMAN], total: 412 }),
    });

    await screen.findByText("expensify.example");
    expect(screen.getByText(/412/)).toBeInTheDocument();
  });

  it("sends the domain, the decision and the reason a human typed", async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mount(OPS, {
      [LIST]: () => jsonResponse({ data: [], total: 0 }),
      [WRITE]: (body) => {
        sent.push(body);
        return jsonResponse({
          ...BY_HEURISTIC,
          domain: "expensify.example",
          source: "human",
          reason: "a tool we use, not a customer",
        });
      },
    });

    await screen.findByText(en["blockedDomains.none"]);
    await user.type(
      screen.getByTestId("blocked-domain-input"),
      "mail.expensify.example",
    );
    await user.type(
      screen.getByTestId("blocked-domain-reason"),
      "a tool we use, not a customer",
    );
    await user.click(
      screen.getByRole("button", { name: en["blockedDomains.save"] }),
    );

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({
      domain: "mail.expensify.example",
      admission: "suppressed",
      reason: "a tool we use, not a customer",
    });
    // The server normalizes the domain and the write REPLACES any decision
    // already on it, so what landed is named: without it a sub-domain silently
    // becomes its parent, and a second decision on a listed domain looks like
    // nothing happened.
    expect(await screen.findByRole("status")).toHaveTextContent(
      "expensify.example",
    );
  });

  it("refuses the write until there is a reason somebody can review", async () => {
    const user = userEvent.setup();
    mount(OPS, { [LIST]: () => jsonResponse({ data: [], total: 0 }) });

    await screen.findByText(en["blockedDomains.none"]);
    const save = screen.getByRole("button", {
      name: en["blockedDomains.save"],
    });
    await user.type(
      screen.getByTestId("blocked-domain-input"),
      "expensify.example",
    );
    // A domain with no reason is exactly what the server 422s, and a refusal
    // nobody can explain is one nobody can review — so the card never sends it.
    expect(save).toBeDisabled();
    await user.type(screen.getByTestId("blocked-domain-reason"), "a vendor");
    expect(save).toBeEnabled();
  });

  it("says why the write was refused rather than leaving the form silent", async () => {
    const user = userEvent.setup();
    mount(OPS, {
      [LIST]: () => jsonResponse({ data: [], total: 0 }),
      [WRITE]: () =>
        jsonResponse(
          {
            title: "Unprocessable Entity",
            detail:
              "expected a domain name like example.com; a full email address or a URL is not one",
          },
          422,
        ),
    });

    await screen.findByText(en["blockedDomains.none"]);
    await user.type(
      screen.getByTestId("blocked-domain-input"),
      "someone@expensify.example",
    );
    await user.type(screen.getByTestId("blocked-domain-reason"), "a vendor");
    await user.click(
      screen.getByRole("button", { name: en["blockedDomains.save"] }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      /a full email address or a URL is not one/,
    );
  });

  it("hands a row's flip to the form, because a reversal still owes a reason", async () => {
    const user = userEvent.setup();
    mount(OPS, {
      [LIST]: () => jsonResponse({ data: [BY_HUMAN], total: 1 }),
    });

    await screen.findByText("mckinsey.example");
    // The row's verb is the OPPOSITE of the standing decision, and taking it
    // fills the form rather than writing: the contract demands a reason and a
    // one-click flip has nowhere to get one.
    await user.click(
      within(rowFor("mckinsey.example")).getByRole("button", {
        name: en["blockedDomains.rowRefuse"],
      }),
    );

    expect(screen.getByTestId("blocked-domain-input")).toHaveValue(
      "mckinsey.example",
    );
    expect(screen.getByTestId("blocked-domain-reason")).toHaveValue("");
    expect(screen.getByTestId("blocked-domain-reason")).toHaveFocus();
  });

  it("keeps the list readable for a seat that may not change it, and says so", async () => {
    mount(READER, {
      [LIST]: () => jsonResponse({ data: [BY_HUMAN], total: 1 }),
    });

    // Withheld, never absent: hiding the card from a reader who may read the
    // posture would answer "was this domain refused" with silence.
    await screen.findByText("mckinsey.example");
    const save = screen.getByRole("button", {
      name: en["blockedDomains.save"],
    });
    expect(save).toBeDisabled();
    const denial = screen.getByText(en["blockedDomains.adminOnly"]);
    expect(save.getAttribute("aria-describedby")).toBe(denial.id);
    expect(screen.getByTestId("blocked-domain-input")).toBeDisabled();
  });
});
