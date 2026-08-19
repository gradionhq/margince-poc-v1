/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { StateStrip } from "./company360";

// The company record's readings row draws FIVE slots on every account, and every
// one of them draws even when it has no reading — which is the rule this file
// exists for. A slot that returns null leaves the row shorter by one and the
// reader unable to tell WHICH reading went missing; only an empty state is
// allowed to say there is none.
//
// It mounts StateStrip directly rather than through CompanyScreen: the count and
// the wording of an absent reading are the component's own contract, and reading
// them through the page would mean the fixture had to satisfy a dozen other
// cards to prove anything about this row.

type Organization360 = components["schemas"]["Organization360"];
type StateStripSection = components["schemas"]["Organization360StateStrip"];
type FinanceSummary = components["schemas"]["OrganizationFinanceSummary"];

const NO_CONNECTION: FinanceSummary = {
  organization_id: "o-1",
  state: "no_connection",
};

const organization: components["schemas"]["Organization"] = {
  id: "o-1",
  display_name: "Brandt Automotive GmbH",
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

function view(overrides: Partial<Organization360> = {}): Organization360 {
  return {
    as_of: "2026-08-18T09:00:00Z",
    organization,
    sections_omitted: [],
    ...overrides,
  };
}

// The finance summary is a query of its own, so even a row that asks nothing of
// it needs the route answered: an unstubbed fetch is a rejected promise, and a
// money slot reading "could not be read" for that reason would pass a test
// written about a connection that is simply not set up.
function stubFinance(summary: FinanceSummary | undefined, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const { pathname } = new URL(request.url);
      if (pathname.endsWith("/finance-summary")) {
        return new Response(JSON.stringify(summary ?? NO_CONNECTION), {
          status,
          headers: { "content-type": "application/json" },
        });
      }
      throw new Error(`the strip asked for ${pathname}, which no test stubs`);
    }),
  );
}

// Unmount between tests. Two mounted strips make `findByRole` ambiguous, and the
// failure it reports ("found multiple elements") looks nothing like the leak
// that caused it.
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The real caller (organizations.tsx's CompanyBand) hands the strip its copy
// functions, so the fixtures do too — a strip fed identity functions would draw
// the wire enum and prove nothing about what a reader sees.
function renderStrip(three60: Organization360) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <StateStrip
          orgId="o-1"
          view={three60}
          lifecycleLabel={(value) => `Lifecycle:${value}`}
          relationshipLabels={(values) => values.join(" · ")}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

async function readings() {
  const region = await screen.findByRole("region", {
    name: "Where this account stands",
  });
  const plate = within(region).getByTestId("company-strip");
  return { region, plate };
}

const prospect: StateStripSection = {
  account: { lifecycle: "prospect", relationship_types: [] },
  commercial: {
    open_count: 1,
    stalled_count: 0,
    priced_count: 1,
    converted_count: 0,
    open_pipeline_minor_base: 100_000,
    base_currency: "EUR",
    next_close_on: "2026-09-30",
  },
};

const customer: StateStripSection = {
  ...prospect,
  account: { lifecycle: "customer", relationship_types: ["customer"] },
};

describe("the company readings row is the shared strip, not a copy of it", () => {
  it("draws the design system's plate and none of its own", async () => {
    stubFinance(NO_CONNECTION);
    const { container } = renderStrip(view({ state_strip: prospect }));
    const { plate } = await readings();

    // The primitive by its own class, which is what says the row folds, rules
    // and sizes its slots the way the person record's row does. A second
    // spelling of the plate is what this row used to be.
    expect(plate.classList.contains("stat-strip")).toBe(true);
    expect(container.querySelector(".co-strip")).toBeNull();
  });

  // Five on every account, because StatStrip counts the children it is handed
  // and derives the fold from that count. A row that sometimes carried six
  // would fold at a different width from one click away on the same account.
  it("carries five slots for a prospect and five for a customer", async () => {
    stubFinance(NO_CONNECTION);
    renderStrip(view({ state_strip: prospect }));
    expect((await readings()).plate.childElementCount).toBe(5);

    cleanup();
    renderStrip(view({ state_strip: customer }));
    expect((await readings()).plate.childElementCount).toBe(5);
  });

  // The lifecycle swap, from the row's own side: a customer trades the prospect's
  // "when does it land" for "what has it been worth", and neither ever draws
  // both or neither.
  it("swaps expected close for the money reading on a customer", async () => {
    stubFinance(NO_CONNECTION);
    renderStrip(view({ state_strip: prospect }));
    const asProspect = await readings();
    expect(within(asProspect.plate).getByText("Expected close")).toBeTruthy();
    expect(within(asProspect.plate).queryByText("Finance")).toBeNull();

    cleanup();
    renderStrip(view({ state_strip: customer }));
    const asCustomer = await readings();
    expect(
      await within(asCustomer.plate).findByText("Connect your accounting"),
    ).toBeTruthy();
    expect(within(asCustomer.plate).getByText("Finance")).toBeTruthy();
    expect(within(asCustomer.plate).queryByText("Expected close")).toBeNull();
  });
});

describe("a slot with no reading says which absence it is", () => {
  // An account nobody has worked: the deal grant is held, so the readings are
  // facts about the ACCOUNT rather than about the reader.
  const bare: StateStripSection = {
    account: { lifecycle: "prospect", relationship_types: [] },
    commercial: {
      open_count: 0,
      stalled_count: 0,
      priced_count: 0,
      converted_count: 0,
    },
  };

  it("still draws five slots when nothing has a figure", async () => {
    stubFinance(NO_CONNECTION);
    renderStrip(view({ state_strip: bare }));
    const { plate } = await readings();

    expect(plate.childElementCount).toBe(5);
    // Every slot, labelled and answered. A blank slot on a ruled plate reads as
    // a reading that failed to load rather than one the account does not have.
    for (const slot of plate.children) {
      expect(slot.querySelector(".stat-card-label")?.textContent).toBeTruthy();
      expect(slot.querySelector(".stat-card-value")?.textContent).toBeTruthy();
    }
  });

  it("names an unrated health reading rather than dropping the slot", async () => {
    stubFinance(NO_CONNECTION);
    renderStrip(view({ state_strip: bare }));
    const { plate } = await readings();

    // Two different absences and two different words. Nothing rated is a
    // denominator ("0 of 3 rated"), and no open deal is an answer.
    expect(within(plate).getByText("Health")).toBeTruthy();
    expect(within(plate).getByText("0 of 3 rated")).toBeTruthy();
    expect(within(plate).getByText("No open deals")).toBeTruthy();
    expect(within(plate).getByText("No date")).toBeTruthy();
    // And no verdict borrowed from nowhere: "at risk" is a rating, and the
    // account has none.
    expect(plate.textContent).not.toMatch(/At risk|Good|Strong/);
  });

  // The half that must never be confused with the half above: a withheld
  // section is a fact about the READER, and reporting it as the account's own
  // standing is the business conclusion a rep would act on.
  it("says a withheld reading is withheld, never that there is none", async () => {
    stubFinance(NO_CONNECTION);
    renderStrip(
      view({
        state_strip: {
          account: { lifecycle: "prospect", relationship_types: [] },
          commercial: null,
        },
        sections_omitted: ["health"],
      }),
    );
    const { plate } = await readings();

    expect(plate.childElementCount).toBe(5);
    // Four of the five read from a section this caller may not see — pipeline
    // and expected close from the deal grant, both health readings from the
    // health grant — so all four say so, and none of them says the account has
    // no deals, no date, no correspondence or no verdict. Only the stage, which
    // comes with the strip itself, still carries a reading.
    expect(within(plate).getAllByText("Not shown").length).toBe(4);
    expect(within(plate).getByText("Lifecycle:prospect")).toBeTruthy();
    expect(within(plate).queryByText("No open deals")).toBeNull();
    expect(within(plate).queryByText("No date")).toBeNull();
    expect(plate.textContent).not.toMatch(/never written/i);
    // A withheld health section has no denominator either: "0 of 3 rated" would
    // be a count of what this reader is allowed to see, dressed as a count of
    // what has been judged.
    expect(within(plate).queryByText("0 of 3 rated")).toBeNull();
  });
});

// The relationship types qualify the lifecycle; they do not restate it. An
// account that IS a customer and also carries the customer relationship type was
// drawing the same word twice in one slot, which reads as two readings that
// happen to agree rather than as one reading with nothing to add.
//
// Both label functions come from the caller, so the fixtures below map the wire
// enum the way production does — a test whose two labels could never collide
// could not see the defect at all.
describe("the account slot does not say the same word twice", () => {
  const titleCase = (value: string) =>
    value.charAt(0).toUpperCase() + value.slice(1);
  const renderWithRealLabels = (section: StateStripSection) =>
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider initial="en">
          <StateStrip
            orgId="o-1"
            view={view({ state_strip: section })}
            lifecycleLabel={titleCase}
            relationshipLabels={(values) => values.map(titleCase).join(" · ")}
          />
        </LocaleProvider>
      </QueryClientProvider>,
    );

  it("drops a relationship detail that only repeats the lifecycle", async () => {
    stubFinance(NO_CONNECTION);
    renderWithRealLabels({
      ...prospect,
      account: { lifecycle: "customer", relationship_types: ["customer"] },
    });
    const { plate } = await readings();
    expect(within(plate).getAllByText("Customer")).toHaveLength(1);
  });

  it("keeps a relationship detail that adds something", async () => {
    stubFinance(NO_CONNECTION);
    renderWithRealLabels({
      ...prospect,
      account: { lifecycle: "customer", relationship_types: ["partner"] },
    });
    const { plate } = await readings();
    expect(within(plate).getByText("Customer")).toBeTruthy();
    expect(within(plate).getByText("Partner")).toBeTruthy();
  });
});
