/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { LeadManualSignals } from "./leadsignals";

// The manual half of the lead score, asked as three plain questions. What the
// suite is here to hold is the wire: a rep who answers a question and presses
// save must not have a claim about evidence quality or certainty recorded in
// their name, and the provenance the contract requires must still arrive.

type Write = { url: string; method: string; body: unknown };

/** The factor a PUT body names, "" for anything that is not one of them. */
function factorOf(body: unknown): string {
  return body && typeof body === "object" && "factor" in body
    ? String(body.factor)
    : "";
}

/**
 * The signal routes. `refuse` decides per PUT whether the server turns that
 * write down — a decision rather than a factor name, so a case can put a
 * refusal in front of the rep once and then let the retry through.
 */
function backend(writes: Write[], refuse?: (factor: string) => boolean) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request ? request.method : (init?.method ?? "GET");
    const raw = request ? await request.text() : String(init?.body ?? "");
    const body: unknown = raw ? JSON.parse(raw) : undefined;
    if (method !== "GET") {
      writes.push({ url, method, body });
    }
    if (method === "PUT" && refuse?.(factorOf(body))) {
      return new Response(
        JSON.stringify({ title: "Unprocessable", status: 422 }),
        {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    }
    if (url.includes("/manual-signals") && method === "GET") {
      return new Response(JSON.stringify({ data: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify({ factor: "web_traffic" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
}

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

async function show(writes: Write[], refuse?: (factor: string) => boolean) {
  vi.stubGlobal("fetch", backend(writes, refuse));
  render(
    <Providers>
      <LeadManualSignals id="l-1" />
    </Providers>,
  );
  // The questions render only once the stored signals have settled — before
  // that the card cannot say what is already set.
  await screen.findByRole("combobox", { name: "Website traffic?" });
}

function question(name: string): HTMLElement {
  return screen.getByRole("combobox", { name });
}

function save(): HTMLElement {
  return screen.getByRole("button", { name: "Add to the score" });
}

/** The PUT bodies, in the order the screen sent them. */
function sentSignals(writes: Write[]): unknown[] {
  return writes.filter((write) => write.method === "PUT").map((w) => w.body);
}

/** The factor each PUT carried, in the order the screen sent them. */
function sentFactors(writes: Write[]): string[] {
  return sentSignals(writes).map(factorOf);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("LeadManualSignals", () => {
  it("asks the three questions the server has factors for, each offering only that factor's bands", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes);

    await user.click(question("Website traffic?"));
    const traffic = screen.getByRole("listbox");
    for (const band of ["Low", "Medium", "High"]) {
      expect(within(traffic).getByRole("option", { name: band })).toBeTruthy();
    }
    // A band belongs to ONE factor: offering an employee count under the
    // traffic question is a 422 the rep cannot see coming.
    expect(within(traffic).queryByRole("option", { name: "201+" })).toBeNull();
    await user.keyboard("{Escape}");

    await user.click(question("Company size?"));
    const size = screen.getByRole("listbox");
    for (const band of ["1–10", "11–50", "51–200", "201+"]) {
      expect(within(size).getByRole("option", { name: band })).toBeTruthy();
    }
    await user.keyboard("{Escape}");

    await user.click(question("Budget?"));
    const budget = screen.getByRole("listbox");
    for (const band of [
      "No budget",
      "Unknown",
      "Some budget",
      "Budget confirmed",
    ]) {
      expect(within(budget).getByRole("option", { name: band })).toBeTruthy();
    }
  });

  it("records an answer nobody qualified as an estimate with no confidence claimed", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes);

    await pickOption(user, question("Website traffic?"), "High");
    await user.click(save());

    await waitFor(() => expect(sentSignals(writes).length).toBe(1));
    expect(sentSignals(writes)[0]).toEqual({
      factor: "web_traffic",
      band: "high",
      // The disclosure was never opened, so the wire says the weakest thing
      // the enum can say and claims no certainty at all.
      signal_kind: "assumption",
      confidence: null,
      reason: "No source given. Entered by hand.",
    });
  });

  it("sends what the rep chose behind More… instead of the defaults", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes);

    const summary = screen.getByText("More…");
    await user.click(summary);
    expect(summary.closest("details")?.open).toBe(true);

    await pickOption(user, question("How reliable is this?"), "Verified");
    await pickOption(user, question("Confidence"), "90% confidence");
    await pickOption(user, question("Budget?"), "Budget confirmed");
    await user.click(save());

    await waitFor(() => expect(sentSignals(writes).length).toBe(1));
    expect(sentSignals(writes)[0]).toEqual({
      factor: "budget_hint",
      band: "confirmed",
      signal_kind: "fact",
      confidence: 0.9,
      reason: "No source given. Entered by hand.",
    });
  });

  it("keeps the note optional and sends it verbatim when there is one", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes);

    await pickOption(user, question("Company size?"), "51–200");
    await user.type(
      screen.getByRole("textbox", { name: "How do you know?" }),
      "  Counted them on their team page.  ",
    );
    await user.click(save());

    await waitFor(() => expect(sentSignals(writes).length).toBe(1));
    expect(sentSignals(writes)[0]).toEqual({
      factor: "employees",
      band: "51-200",
      signal_kind: "assumption",
      confidence: null,
      reason: "Counted them on their team page.",
    });
  });

  it("writes every question the rep answered and none they left alone", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes);

    await pickOption(user, question("Website traffic?"), "Medium");
    await pickOption(user, question("Budget?"), "Some budget");
    await user.click(save());

    await waitFor(() => expect(sentSignals(writes).length).toBe(2));
    expect(sentFactors(writes)).toEqual(["web_traffic", "budget_hint"]);
  });

  it("cannot be saved until a question is answered", async () => {
    const writes: Write[] = [];
    await show(writes);

    expect(save().hasAttribute("disabled")).toBe(true);
  });

  it("says why a refused batch was refused and keeps the answers for a retry", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    await show(writes, (factor) => factor === "budget_hint");

    await pickOption(user, question("Budget?"), "Unknown");
    await user.click(save());

    expect(await screen.findByText(/Unprocessable/)).toBeTruthy();
    // The rep's answer is still on screen: a refusal that also empties the
    // form makes them re-do work the server never took.
    expect(question("Budget?").textContent).toContain("Unknown");
  });

  it("keeps only the outstanding answer after a part-way failure, so a retry re-sends nothing that landed", async () => {
    const user = userEvent.setup();
    const writes: Write[] = [];
    // The second write is refused once and taken the next time. What the retry
    // must not do is repeat the first factor: the server already took it, and
    // a second PUT would re-stamp `set_by`/`set_at` and append a superseding
    // row that records no change.
    let refusalsLeft = 1;
    await show(writes, (factor) => {
      if (factor === "budget_hint" && refusalsLeft > 0) {
        refusalsLeft -= 1;
        return true;
      }
      return false;
    });

    await pickOption(user, question("Website traffic?"), "Medium");
    await pickOption(user, question("Budget?"), "Some budget");
    await user.click(save());

    expect(await screen.findByText(/Unprocessable/)).toBeTruthy();
    expect(sentFactors(writes)).toEqual(["web_traffic", "budget_hint"]);
    // The answer the server took is retired; the one it refused is still there
    // to press save on again.
    await waitFor(() =>
      expect(question("Website traffic?").textContent).not.toContain("Medium"),
    );
    expect(question("Budget?").textContent).toContain("Some budget");

    await user.click(save());

    // The retry lands, the form empties and the verb goes quiet: the settled
    // state, after which no further write can arrive to flatter the log below.
    await waitFor(() => expect(save().hasAttribute("disabled")).toBe(true));
    expect(sentFactors(writes)).toEqual([
      "web_traffic",
      "budget_hint",
      "budget_hint",
    ]);
  });

  it("shows the reason a terminal lead cannot be edited instead of the questions", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(
      <Providers>
        <LeadManualSignals id="l-1" readOnlyReason="This lead is closed." />
      </Providers>,
    );

    expect(await screen.findByText("This lead is closed.")).toBeTruthy();
    expect(
      screen.queryByRole("combobox", { name: "Website traffic?" }),
    ).toBeNull();
  });
});
