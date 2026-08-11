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
import { AgentDock } from "./agentdock";

// The agent dock at the right edge of the page head. Two promises run through
// every case here: what it SHOWS at each density (the resting line, the badge
// for what is waiting, the panel behind the click), and what it may CLAIM — the
// runtime knows routing is configured and has proved nothing about a provider
// being reachable, so no surface of the dock is allowed to read as liveness.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type ToolCatalog = components["schemas"]["AgentToolListResponse"];

const catalogTool = (
  name: string,
  tier: ToolCatalog["data"][number]["tier"],
): ToolCatalog["data"][number] => ({
  name,
  title: name,
  description: `what ${name} does`,
  tier,
  egress: false,
});

// Two that act on their own and one that has to be confirmed, so the summary
// the panel prints ("2 auto · 1 confirm") could not come from a miscount that
// happened to match the total.
const CATALOG: ToolCatalog = {
  data: [
    catalogTool("progress_deal", "auto_execute"),
    catalogTool("enrich", "auto_execute"),
    catalogTool("send_email", "confirmation_required"),
  ],
};

// Seeded through the cache rather than a fetch stub: `useAgentTierMap` reads
// ["agent-tools"], so the envelope GET /agent-tools returns is written straight
// into that entry and the panel renders from the same snapshot the app does.
// `null` leaves the entry empty — the state before the catalog has arrived.
const render = (ui: ReactNode, catalog: ToolCatalog | null = CATALOG) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  if (catalog) {
    client.setQueryData(["agent-tools"], catalog);
  }
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

// The trigger's accessible name leads with who the agent is and carries the
// state line (and, when there is one, the waiting count) after it.
const openDock = async () => {
  const trigger = screen.getByRole("button", { name: /^Margince AI/ });
  await userEvent.click(trigger);
  return trigger;
};

// Any of these words would report that something is running right now, which is
// the one thing no part of the dock is entitled to say.
const LIVENESS = /connected|online|live|running|healthy/i;

describe("AgentDock", () => {
  it("says who the agent is and what state it is in, and never that it is live", () => {
    const { container } = render(<AgentDock />);
    const trigger = screen.getByRole("button", { name: /^Margince AI/ });
    expect(container.querySelector(".agentdocktrigger")).toBe(trigger);
    expect(within(trigger).getByText("Margince AI")).toBeTruthy();
    expect(within(trigger).getByText("Configured")).toBeTruthy();
    expect(trigger.textContent).not.toMatch(LIVENESS);

    // The Core carries the same limit as the sentence: a sphere at work, or one
    // taking a feed, claims liveness just as loudly as the word would.
    expect(
      container.querySelector(".agentorb")?.getAttribute("data-core-state"),
    ).toBe("quiet");
    expect(container.querySelector(".core-feed")).toBeNull();
  });

  it("carries no count badge when nothing is waiting", () => {
    const unloaded = render(<AgentDock />);
    expect(unloaded.container.querySelector(".agentwait")).toBeNull();
    cleanup();

    // Nor for a loaded zero: a badge is what wants attention, and zero does not.
    const empty = render(<AgentDock approvalsWaiting={0} />);
    expect(empty.container.querySelector(".agentwait")).toBeNull();
  });

  // A badge only a sighted user can count is half a signal, so the count is part
  // of the trigger's own name rather than a bare number beside it.
  it("shows a waiting count at rest and says what it counts", () => {
    const { container } = render(<AgentDock approvalsWaiting={3} />);
    const badge = container.querySelector(".agentwait");
    expect(badge?.textContent).toBe("3 Approvals waiting");
    expect(badge?.querySelector(".sr-only")?.textContent).toBe(
      " Approvals waiting",
    );
    expect(
      screen.getByRole("button", { name: /3\s*Approvals waiting$/ }),
    ).toBeTruthy();
  });

  it("opens the panel on the trigger without the opening click closing it again", async () => {
    const { container } = render(<AgentDock approvalsWaiting={3} />);
    expect(container.querySelector(".agentpanel")).toBeNull();

    const trigger = await openDock();
    // Dismissal listens on the document, so a listener armed during the click
    // would see that same click bubble up and shut the panel on the way out.
    const panel = container.querySelector(".agentpanel");
    expect(panel).not.toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(panel?.textContent).not.toMatch(LIVENESS);
    expect(panel?.textContent).toContain("Configured");
  });

  it("closes on a click outside itself", async () => {
    const { container } = render(<AgentDock approvalsWaiting={3} />);
    await openDock();
    expect(container.querySelector(".agentpanel")).not.toBeNull();

    await userEvent.click(document.body);
    expect(container.querySelector(".agentpanel")).toBeNull();
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    const { container } = render(<AgentDock approvalsWaiting={3} />);
    const trigger = await openDock();
    // Standing inside the panel, the way a keyboard user arrives at a row.
    const ask = screen.getByRole("link", { name: "Ask Margince" });
    ask.focus();
    expect(document.activeElement).toBe(ask);

    await userEvent.keyboard("{Escape}");
    expect(container.querySelector(".agentpanel")).toBeNull();
    // Not the body: dismissing unmounts the focused row, and focus left on the
    // body restarts the next Tab at the top of the page.
    expect(document.activeElement).toBe(trigger);
  });

  it("leads the panel with the surface where you talk to the agent", async () => {
    render(<AgentDock approvalsWaiting={3} />);
    await openDock();
    expect(
      screen.getByRole("link", { name: "Ask Margince" }).getAttribute("href"),
    ).toBe("#/ai");
  });

  it("sends the waiting count to the approvals inbox that holds it", async () => {
    render(<AgentDock approvalsWaiting={3} />);
    await openDock();
    const row = screen.getByRole("link", { name: /^Approvals waiting/ });
    expect(row.getAttribute("href")).toBe("#/inbox");
    expect(row.querySelector(".agentvalue")?.textContent).toBe("3");
  });

  // Zero waiting is a live answer, not a missing one: the row stays and prints
  // it. Only the at-rest badge treats zero as nothing to say.
  it("keeps the approvals row for a loaded zero", async () => {
    const { container } = render(<AgentDock approvalsWaiting={0} />);
    await openDock();
    const row = screen.getByRole("link", { name: /^Approvals waiting/ });
    expect(row.querySelector(".agentvalue")?.textContent).toBe("0");
    expect(container.querySelector(".agentwait")).toBeNull();
  });

  it("summarises the tool catalog by tier and links where it is governed", async () => {
    render(<AgentDock approvalsWaiting={3} />);
    await openDock();
    const row = screen.getByRole("link", { name: /^Agent tools/ });
    expect(row.getAttribute("href")).toBe("#/settings/ai");
    expect(row.querySelector(".agentvalue")?.textContent).toBe(
      "2 auto · 1 confirm",
    );
  });

  // "0 waiting" is a claim about this installation, and a count that has not
  // arrived is not one. Both live rows are absent while there is nothing to
  // read, and each case is asserted against a panel where the OTHER row is
  // present — absence has to be that row's own, not the panel failing to open.
  it("omits the approvals row rather than standing in a zero for an unread count", async () => {
    render(<AgentDock />);
    await openDock();
    expect(
      screen.queryByRole("link", { name: /^Approvals waiting/ }),
    ).toBeNull();
    expect(screen.getByRole("link", { name: /^Agent tools/ })).toBeTruthy();
  });

  it("omits the tool row rather than reporting an empty catalog it has not read", async () => {
    // No cache entry to read, so the hook goes to the wire; the request is
    // answered with the failure that leaves the catalog unread, and the row has
    // to stay away both before and after that answer lands.
    const unavailable = vi.fn(
      async () =>
        new Response(JSON.stringify({ code: "unavailable" }), {
          status: 503,
          headers: { "Content-Type": "application/problem+json" },
        }),
    );
    vi.stubGlobal("fetch", unavailable);

    render(<AgentDock approvalsWaiting={3} />, null);
    await openDock();
    expect(screen.queryByRole("link", { name: /^Agent tools/ })).toBeNull();

    await waitFor(() => expect(unavailable).toHaveBeenCalled());
    expect(screen.queryByRole("link", { name: /^Agent tools/ })).toBeNull();
    expect(
      screen.getByRole("link", { name: /^Approvals waiting/ }),
    ).toBeTruthy();
  });

  // Activity, routing and spend have no handler behind them. The marker is what
  // keeps them from passing as real, so it has to be readable rather than
  // announced-only — clipped to `.sr-only` it labels the block for a screen
  // reader and leaves everyone else looking at invented numbers.
  it("marks the example block before the values a reader would take as real", async () => {
    render(<AgentDock approvalsWaiting={3} />);
    await openDock();

    const marker = screen.getByText("Example data");
    expect(marker.className).not.toContain("sr-only");
    expect(marker.closest("[aria-hidden]")).toBeNull();

    const block = marker.closest(".agentexample");
    for (const example of [
      "Enriching 4 new contacts",
      "Local + cloud",
      "€2.41",
    ]) {
      expect(block?.textContent).toContain(example);
    }
    // The marker leads the block, so the values are labelled before they read.
    expect(
      marker.compareDocumentPosition(screen.getByText("Local + cloud")) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
  });
});
