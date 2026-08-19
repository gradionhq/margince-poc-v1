/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { AgentDock } from "./agentdock";
import { ASK_QUERY_KEY } from "./palette";
import type { Route } from "./router";

// The agent dock, floating at the foot of the content column. Three promises
// run through the cases here: what it SHOWS at each density (the resting line,
// the badge for what is waiting, the panel behind the click); what it may CLAIM
// — the runtime knows routing is configured and has proved nothing about a
// provider being reachable, so no surface of the dock is allowed to read as
// liveness; and what it OFFERS — the record-scoped ask it absorbed from the
// "Ask about this" FAB (B-EP09.6, AC-shell-8), whose scope copy is the limit on
// what any answer can be drawn from.

// Every case that is not about the route itself stands on one, and a list
// screen is the ordinary one: the composer is present and names its screen.
const ROUTE: Route = { screen: "deals" };

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
const render = (
  ui: ReactNode,
  catalog: ToolCatalog | null = CATALOG,
  // Record names the app has already read. The composer names what it will be
  // asked ABOUT, and that answer is a read (app/pagemeta.ts) — seeded here the
  // way the app holds it, so a case can show the resolved name and the case
  // beside it can show what stands in while nothing has resolved.
  names: Readonly<Record<string, string>> = {},
) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  if (catalog) {
    client.setQueryData(["agent-tools"], catalog);
  }
  for (const [key, name] of Object.entries(names)) {
    const [kind, id] = key.split(":");
    client.setQueryData([kind, "ref", id], name);
  }
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

// The trigger's accessible name leads with who the agent is and carries the
// state line (and, when there is one, the waiting count) after it.
// One `userEvent.setup()` per test, threaded through here rather than reached for
// from the module: the instance carries that test's pointer and keyboard state,
// and a module-level call quietly makes a fresh one for every interaction.
const openDock = async (user: UserEvent) => {
  const trigger = screen.getByRole("button", { name: /^Margince AI/ });
  await user.click(trigger);
  return trigger;
};

// Any of these words would report that something is running right now, which is
// the one thing no part of the dock is entitled to say.
const LIVENESS = /connected|online|live|running|healthy/i;

describe("AgentDock", () => {
  it("says who the agent is and what state it is in, and never that it is live", () => {
    const { container } = render(<AgentDock route={ROUTE} />);
    const trigger = screen.getByRole("button", { name: /^Margince AI/ });
    expect(container.querySelector(".agentdocktrigger")).toBe(trigger);
    expect(within(trigger).getByText("Margince AI")).toBeTruthy();
    expect(within(trigger).getByText("Configured")).toBeTruthy();
    expect(trigger.textContent).not.toMatch(LIVENESS);

    // The Core carries the same limit as the sentence: a sphere at work, or one
    // taking a feed, claims liveness just as loudly as the word would.
    expect(
      container.querySelector(".agentorb")?.getAttribute("data-core-state"),
    ).toBe("dormant");
    expect(container.querySelector(".core-feed")).toBeNull();
  });

  it("carries no count badge when nothing is waiting", () => {
    const unloaded = render(<AgentDock route={ROUTE} />);
    expect(unloaded.container.querySelector(".agentwait")).toBeNull();
    cleanup();

    // Nor for a loaded zero: a badge is what wants attention, and zero does not.
    const empty = render(<AgentDock route={ROUTE} approvalsWaiting={0} />);
    expect(empty.container.querySelector(".agentwait")).toBeNull();
  });

  // A badge only a sighted user can count is half a signal, so the count is part
  // of the trigger's own name rather than a bare number beside it.
  it("shows a waiting count at rest and says what it counts", () => {
    const { container } = render(
      <AgentDock route={ROUTE} approvalsWaiting={3} />,
    );
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
    const user = userEvent.setup();
    const { container } = render(
      <AgentDock route={ROUTE} approvalsWaiting={3} />,
    );
    expect(container.querySelector(".agentpanel")).toBeNull();

    const trigger = await openDock(user);
    // Dismissal listens on the document, so a listener armed during the click
    // would see that same click bubble up and shut the panel on the way out.
    const panel = container.querySelector(".agentpanel");
    expect(panel).not.toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(panel?.textContent).not.toMatch(LIVENESS);
    expect(panel?.textContent).toContain("Configured");
  });

  it("closes on a click outside itself", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <AgentDock route={ROUTE} approvalsWaiting={3} />,
    );
    await openDock(user);
    expect(container.querySelector(".agentpanel")).not.toBeNull();

    await user.click(document.body);
    expect(container.querySelector(".agentpanel")).toBeNull();
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <AgentDock route={ROUTE} approvalsWaiting={3} />,
    );
    const trigger = await openDock(user);
    // Standing inside the panel, the way a keyboard user arrives at a row.
    const ask = screen.getByRole("link", { name: "Ask Margince" });
    ask.focus();
    expect(document.activeElement).toBe(ask);

    await user.keyboard("{Escape}");
    expect(container.querySelector(".agentpanel")).toBeNull();
    // Not the body: dismissing unmounts the focused row, and focus left on the
    // body restarts the next Tab at the top of the page.
    expect(document.activeElement).toBe(trigger);
  });

  // Narrowest first: the question about the record you are standing on, then
  // the link to the surface where anything wider is asked. Reversed, the link
  // reads as the only way to ask and the composer under it as an afterthought.
  it("puts the scoped composer above the link to the full Ask surface", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={ROUTE} approvalsWaiting={3} />);
    await openDock(user);
    const ask = screen.getByRole("link", { name: "Ask Margince" });
    expect(ask.getAttribute("href")).toBe("#/ai");
    expect(
      screen
        .getByRole("textbox", { name: "Your question" })
        .compareDocumentPosition(ask) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeGreaterThan(0);
  });

  it("sends the waiting count to the approvals inbox that holds it", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={ROUTE} approvalsWaiting={3} />);
    await openDock(user);
    const row = screen.getByRole("link", { name: /^Approvals waiting/ });
    expect(row.getAttribute("href")).toBe("#/inbox");
    expect(row.querySelector(".agentvalue")?.textContent).toBe("3");
  });

  // Zero waiting is a live answer, not a missing one: the row stays and prints
  // it. Only the at-rest badge treats zero as nothing to say.
  it("keeps the approvals row for a loaded zero", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <AgentDock route={ROUTE} approvalsWaiting={0} />,
    );
    await openDock(user);
    const row = screen.getByRole("link", { name: /^Approvals waiting/ });
    expect(row.querySelector(".agentvalue")?.textContent).toBe("0");
    expect(container.querySelector(".agentwait")).toBeNull();
  });

  it("summarises the tool catalog by tier and links where it is governed", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={ROUTE} approvalsWaiting={3} />);
    await openDock(user);
    const row = screen.getByRole("link", { name: /^Agent tools/ });
    expect(row.getAttribute("href")).toBe("#/settings/agents");
    expect(row.querySelector(".agentvalue")?.textContent).toBe(
      "2 auto · 1 confirm",
    );
  });

  // "0 waiting" is a claim about this installation, and a count that has not
  // arrived is not one. Both live rows are absent while there is nothing to
  // read, and each case is asserted against a panel where the OTHER row is
  // present — absence has to be that row's own, not the panel failing to open.
  it("omits the approvals row rather than standing in a zero for an unread count", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={ROUTE} />);
    await openDock(user);
    expect(
      screen.queryByRole("link", { name: /^Approvals waiting/ }),
    ).toBeNull();
    expect(screen.getByRole("link", { name: /^Agent tools/ })).toBeTruthy();
  });

  it("omits the tool row rather than reporting an empty catalog it has not read", async () => {
    const user = userEvent.setup();
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

    render(<AgentDock route={ROUTE} approvalsWaiting={3} />, null);
    await openDock(user);
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
    const user = userEvent.setup();
    render(<AgentDock route={ROUTE} approvalsWaiting={3} />);
    await openDock(user);

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

// The composer the dock absorbed from the "Ask about this" FAB (B-EP09.6,
// AC-shell-8). The acceptance criterion did not go away when the FAB did, only
// the element carrying it — these cases moved here from palette.test.tsx with
// the component they describe.
describe("the record-scoped ask (AC-shell-8)", () => {
  it("names the screen the dock was opened on", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={{ screen: "deals" }} />);
    await openDock(user);
    expect(screen.getByText("Ask about Pipeline")).toBeTruthy();
  });

  // The record by NAME, in the same words the trail at the top of the window
  // uses. The dock resolved this separately once and printed the route's raw id,
  // so the agent offered to answer questions about `01a01811-c847-…` while the
  // trail two inches above it said "Carol Wagner".
  it("names the record when there is one, over the screen holding it", async () => {
    const user = userEvent.setup();
    render(
      <AgentDock route={{ screen: "companies", id: "brandt" }} />,
      CATALOG,
      { "organization:brandt": "Brandt Logistik GmbH" },
    );
    await openDock(user);
    expect(screen.getByText("Ask about Brandt Logistik GmbH")).toBeTruthy();
  });

  // The composer's Send goes somewhere: the question is handed to the Ask
  // surface through the seam the palette's own ask command uses, and the reader
  // goes with it. A primary button on permanent chrome that accepts a press and
  // does nothing is the defect this pins.
  it("hands the question to the Ask surface and takes the reader there", async () => {
    const user = userEvent.setup();
    window.location.hash = "#/deals";
    sessionStorage.removeItem(ASK_QUERY_KEY);
    render(<AgentDock route={{ screen: "deals" }} />);
    await openDock(user);
    await user.type(
      screen.getByRole("textbox", { name: "Your question" }),
      "  which deals slipped?  ",
    );
    await user.click(screen.getByRole("button", { name: "Ask" }));
    expect(sessionStorage.getItem(ASK_QUERY_KEY)).toBe("which deals slipped?");
    expect(window.location.hash).toBe("#/ai");
  });

  // Nothing typed, nothing to send — and the button says which rather than
  // taking a press and dropping it.
  it("refuses an empty question and says why", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={{ screen: "deals" }} />);
    await openDock(user);
    const send = screen.getByRole("button", { name: "Ask" });
    expect(send.hasAttribute("disabled")).toBe(true);
    const reason = send.getAttribute("aria-describedby");
    expect(reason).toBeTruthy();
    expect(document.getElementById(reason ?? "")?.textContent).toBe(
      "Write a question first.",
    );
  });

  // A read still in flight: the id is not a name, but it is true, and it is what
  // the reader can quote. A composer that named nothing would be a question about
  // the whole product.
  //
  // The read is pinned rather than left to the environment. Seeded with no name
  // the query runs for real, and what it does then is the runner's business — a
  // machine with no network rejects it in a millisecond and the panel says the
  // name did not load, which is the case BELOW, not this one. A read that never
  // answers is what "has not resolved" means, so that is what this hands it.
  it("falls back to the record id while the name is still coming", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", () => new Promise(() => {}));
    render(<AgentDock route={{ screen: "companies", id: "brandt" }} />);
    await openDock(user);
    expect(screen.getByText("Ask about brandt")).toBeTruthy();
  });

  // A read that will never arrive is a different sentence, and it may not borrow
  // the id: painting it for a refused or failed read states as settled fact a
  // question nothing answered (screens/entityref.tsx).
  it("says the name did not load when the read failed, and never the id", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", () => Promise.reject(new Error("offline")));
    render(<AgentDock route={{ screen: "companies", id: "brandt" }} />);
    await openDock(user);
    await waitFor(() =>
      expect(screen.getByText("Ask about Name didn't load")).toBeTruthy(),
    );
    expect(screen.queryByText("Ask about brandt")).toBeNull();
  });

  // The agent reads only the RBAC ∩ Passport intersection. This sentence is the
  // one place the panel says so, which is why it ships with the composer rather
  // than near it.
  it("carries the load-bearing scope copy alongside the input and its verb", async () => {
    const user = userEvent.setup();
    render(<AgentDock route={{ screen: "home" }} />);
    await openDock(user);
    expect(
      screen.getByText("Your agent reads only what you can see."),
    ).toBeTruthy();
    expect(screen.getByRole("textbox", { name: "Your question" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Ask" })).toBeTruthy();
  });

  // On the full Ask surface a scoped composer would offer to ask about the page
  // the reader is already asking on. The DOCK still renders there — it is the
  // one floating AI element the shell has — so its absence must be the
  // composer's own, not the panel failing to open.
  it("offers no composer on the full Ask surface, and still reports what is waiting", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <AgentDock route={{ screen: "ai" }} approvalsWaiting={3} />,
    );
    expect(container.querySelector(".agentwait")?.textContent).toBe(
      "3 Approvals waiting",
    );

    await openDock(user);
    expect(container.querySelector(".agentask")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Your question" })).toBeNull();
    expect(screen.getByRole("link", { name: "Ask Margince" })).toBeTruthy();
    expect(
      screen.getByRole("link", { name: /^Approvals waiting/ }),
    ).toBeTruthy();
  });
});

// Where the dock STANDS is a promise too: it floats at the foot of the content
// column, so its panel has to open upward or fall off the bottom of the page,
// and on a phone it has to clear the floating navigation bar sharing that
// ledge. jsdom cannot answer any of that — vitest does not apply the imported
// stylesheet, so getComputedStyle reports the UA default for every rule in it.
// The stylesheet is the artefact that decides these, so the stylesheet is what
// is read, the same shape the design-system conformance suite uses.
//
// Read from disk rather than imported: vitest runs with `css: false`, so an
// imported stylesheet — `?raw` included — arrives as an empty string. The path
// is relative to the frontend package, which is vitest's root; `import.meta.url`
// is no help because under the jsdom environment it is an http: URL.
const STYLESHEET = readFileSync("src/app/agentdock.css", "utf8").replace(
  /\/\*[\s\S]*?\*\//g,
  "",
);

const ruleFor = (selector: string): string => {
  const start = STYLESHEET.indexOf(`${selector} {`);
  expect(start, `no \`${selector}\` rule in agentdock.css`).toBeGreaterThan(-1);
  return STYLESHEET.slice(start, STYLESHEET.indexOf("}", start));
};

describe("where the dock stands", () => {
  it("floats centred on the foot of the column it is positioned against", () => {
    const dock = ruleFor(".agentdock");
    expect(dock).toContain("position: absolute");
    expect(dock).toContain("left: 50%");
    expect(dock).toContain("transform: translateX(-50%)");
    expect(dock).toContain("bottom: var(--space-6)");
  });

  it("opens the panel upward from the trigger, not down off the page", () => {
    const panel = ruleFor(".agentpanel");
    expect(panel).toContain("bottom: calc(100% +");
    // `top` and `bottom` together would stretch the panel between them and
    // silently override the height its content asked for.
    expect(panel).not.toMatch(/\btop:/);
    expect(panel).toContain("left: 50%");
    // A panel wider than the viewport is a panel with a piece missing.
    expect(panel).toContain("max-width: calc(100vw - 2 * var(--space-4))");
  });

  it("stands clear of the phone layout's floating bottom bar", () => {
    // That bar sits on `max(var(--space-2), env(safe-area-inset-bottom))` and is
    // ~56px tall, so the dock owes it both a fixed clearance and the inset the
    // bar has already spent.
    const offset =
      /bottom:\s*calc\((\d+)px \+ env\(safe-area-inset-bottom\)\)/.exec(
        STYLESHEET,
      );
    expect(
      offset,
      "no phone-width bottom offset in agentdock.css",
    ).not.toBeNull();
    expect(Number(offset?.[1])).toBeGreaterThanOrEqual(76);
  });
});
