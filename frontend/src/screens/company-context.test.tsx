/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { CompanyContextCard } from "./company-context";

type Capabilities = components["schemas"]["CompanyContextCapabilities"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type SiteRead = components["schemas"]["CompanySiteRead"];

// read_enabled is the card's own gate — it renders nothing below the `read`
// rollout stage, so the fixture has to clear it before any assertion is
// reachable.
const CAPABILITIES: Capabilities = {
  rollout: "onboarding",
  read_enabled: true,
  tasks_enabled: true,
  onboarding_enabled: true,
};

// A website is what arms the refresh control; without one the button stays
// disabled and no comparison ever reaches the screen.
const COMPANY: CompanyProfile = {
  organization_id: "00000000-0000-4000-8000-000000000010",
  display_name: "Acme",
  website: "acme.test",
  offer_summary: "We sell field service software",
  icp: "Operations leads at mid-market installers",
};

// One comparison per classification. The two selectable ones deliberately
// carry field keys whose labels read nothing alike ("What do you sell?" vs
// "Registered legal name"), because the defect this guards — one shared,
// field-less label on every checkbox — is invisible whenever a row is
// inspected on its own.
const COMPARISONS: readonly Comparison[] = [
  {
    key: "offer_summary",
    value_kind: "profile_field",
    classification: "new",
    current_value: null,
    current_source: null,
    proposed_value: "Field service software for installers",
  },
  {
    key: "legal_name",
    value_kind: "profile_field",
    classification: "machine_change",
    current_value: "Acme Ltd",
    current_source: "site_read",
    proposed_value: "Acme GmbH",
  },
  {
    key: "icp",
    value_kind: "profile_field",
    classification: "human_conflict",
    current_value: "Operations leads at mid-market installers",
    current_source: "human",
    proposed_value: "Enterprise facility managers",
  },
  {
    key: "industry",
    value_kind: "profile_field",
    classification: "unchanged",
    current_value: "Software",
    current_source: "human",
    proposed_value: "Software",
  },
];

// `ready` is terminal for the poller: the review renders once and the query
// stops refetching, so the suite never waits on a clock.
const SITE_READ: SiteRead = {
  id: "00000000-0000-4000-8000-000000000020",
  target_kind: "onboarding",
  root_url: "https://acme.test",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  pages: [{ url: "https://acme.test", status: "fetched", kind: "home" }],
  profile_fields: [],
  facts: [],
  comparisons: [...COMPARISONS],
  people: [],
  warnings: [],
  draft_version: 3,
  proposal_hash: "sha256:acme",
  created_at: "2026-01-05T09:00:00Z",
  updated_at: "2026-01-05T09:04:00Z",
};

function backend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const body = routeBody(url.pathname);
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
}

function routeBody(path: string): unknown {
  if (path === "/v1/company/context/capabilities") {
    return CAPABILITIES;
  }
  // Starting a read and polling it both answer with the whole read, so one
  // body serves POST /site-reads and GET /site-reads/{id}.
  if (path.startsWith("/v1/company/site-reads")) {
    return SITE_READ;
  }
  if (path === "/v1/company") {
    return COMPANY;
  }
  throw new Error(`unstubbed request: ${path}`);
}

function Providers({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Reaching the review costs two sequential round trips — the POST that starts
// the read, then the GET that fetches it — each landing through react-query's
// own state cycle. Nothing here waits on a clock: `ready` is terminal, so the
// poller's refetchInterval is false and the chain settles as fast as the
// scheduler runs it. What varies is only how quickly that is, and the default
// one-second budget is not enough when the whole suite runs in parallel, so
// this states a budget that survives a loaded machine.
//
// Generous on purpose, and the size is the point rather than a guess: this file
// shares a machine with every other suite, and the slowest lane — coverage
// instrumentation over the whole tree — runs about an order of magnitude behind a
// developer's own `vitest run`. A budget picked against the fast case turns a
// scheduler that was merely busy into a failure that names an element instead of
// the load, which is a test reporting on the machine rather than on the card.
const SETTLE_MS = 20_000;

// The budget for a test that drives `renderReview`, and it must cover EVERY
// waiter in there: three run in sequence, each bounded by SETTLE_MS. A test whose
// own limit is smaller than the sum lets vitest fire while a waiter still has
// budget, and what surfaces then is an opaque timeout rather than the assertion
// the test was written to make. Vitest's per-test default is 5s, which is less
// than one of these waiters alone.
const TEST_MS = SETTLE_MS * 3;

// The fixture's two selectable changes — the `new` and `machine_change` rows.
// The human_conflict row is decided by radio and the unchanged row offers
// nothing, so neither carries a checkbox.
const SELECTABLE = 2;

// Renders the card and drives it to the review step, where the comparison
// cards live.
async function renderReview() {
  vi.stubGlobal("fetch", backend());
  render(
    <Providers>
      <CompanyContextCard />
    </Providers>,
  );
  const refresh = await screen.findByRole(
    "button",
    { name: "Refresh from website" },
    { timeout: SETTLE_MS },
  );
  fireEvent.click(refresh);
  await screen.findByRole(
    "heading",
    { name: "Review what changed" },
    { timeout: SETTLE_MS },
  );
  // The heading is not the last thing to arrive: the comparison cards commit
  // from the site read that the heading only announces, so waiting on the
  // heading alone leaves every assertion below racing the rows it reads. Wait
  // for the rows themselves — the fixture's two selectable changes — so the
  // test is settled rather than merely started.
  await waitFor(
    () => expect(screen.getAllByRole("checkbox")).toHaveLength(SELECTABLE),
    { timeout: SETTLE_MS },
  );
}

describe("CompanyContextCard refresh review", () => {
  it(
    "names the field each change checkbox selects",
    async () => {
      await renderReview();

      // Two checkboxes, two accessible names. getByRole is exact and unique, so
      // a shared label — every row announcing the same words — fails both
      // lookups rather than passing one of them.
      expect(
        screen.getByRole("checkbox", {
          name: "Select the What do you sell? change",
        }),
      ).toBeTruthy();
      expect(
        screen.getByRole("checkbox", {
          name: "Select the Registered legal name change",
        }),
      ).toBeTruthy();

      const names = screen
        .getAllByRole("checkbox")
        .map((box) => box.getAttribute("aria-label"));
      expect(new Set(names).size).toBe(names.length);
    },
    TEST_MS,
  );

  it(
    "offers a checkbox only for a change that can be selected",
    async () => {
      await renderReview();

      // A human conflict is decided by radio, not selected by checkbox, and an
      // unchanged value has nothing to apply — a checkbox on either would write
      // a change the reviewer never chose.
      expect(screen.getAllByRole("checkbox")).toHaveLength(SELECTABLE);
      expect(
        screen.queryByRole("checkbox", { name: /Ideal customer/ }),
      ).toBeNull();
      expect(screen.queryByRole("checkbox", { name: /Industry/ })).toBeNull();
      expect(screen.getByRole("radio", { name: "Keep current" })).toBeTruthy();
    },
    TEST_MS,
  );
});

// Two failures reach the same paragraph and only one of them was written for
// the person reading it: the start POST answers the URL they just typed, while
// a status poll answers a read id they never saw.
describe("CompanyContextCard refresh failures", () => {
  // The site-read routes split by method here: starting a read and polling it
  // share a path prefix, and the whole distinction under test is which of the
  // two failed.
  function backendWithSiteReads(
    start: () => Response,
    poll: () => Response,
  ): typeof fetch {
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request =
        input instanceof Request ? input : new Request(String(input), init);
      const path = new URL(request.url).pathname;
      if (path.startsWith("/v1/company/site-reads")) {
        return request.method === "POST" ? start() : poll();
      }
      return new Response(JSON.stringify(routeBody(path)), {
        headers: { "Content-Type": "application/json" },
      });
    });
  }

  function problemResponse(detail: string, status: number) {
    return new Response(JSON.stringify({ detail, status }), {
      status,
      headers: { "Content-Type": "application/problem+json" },
    });
  }

  async function clickRefresh(stub: typeof fetch) {
    vi.stubGlobal("fetch", stub);
    render(
      <Providers>
        <CompanyContextCard />
      </Providers>,
    );
    const refresh = await screen.findByRole(
      "button",
      { name: "Refresh from website" },
      { timeout: SETTLE_MS },
    );
    // The button appears as soon as the profile lands, but the start it fires
    // reads the website out of the form state — so the click waits for the
    // control that holds it rather than for the button alone.
    await waitFor(
      () =>
        expect(
          screen.getByLabelText<HTMLInputElement>("Public company website")
            .value,
        ).toBe(COMPANY.website),
      { timeout: SETTLE_MS },
    );
    await userEvent.click(refresh);
  }

  it("quotes the server when the start itself was refused", async () => {
    await clickRefresh(
      backendWithSiteReads(
        () => problemResponse("That site refuses automated readers.", 422),
        () => problemResponse("site read not found", 404),
      ),
    );

    expect(
      await screen.findByText(
        "That site refuses automated readers.",
        undefined,
        { timeout: SETTLE_MS },
      ),
    ).toBeTruthy();
  });

  it("keeps a failed status poll to the catalog sentence", async () => {
    await clickRefresh(
      backendWithSiteReads(
        () =>
          new Response(JSON.stringify(SITE_READ), {
            headers: { "Content-Type": "application/json" },
          }),
        () => problemResponse("site_read 0000-0020 row not visible", 404),
      ),
    );

    expect(
      await screen.findByText(
        "We lost track of this website read. Start the refresh again.",
        undefined,
        { timeout: SETTLE_MS },
      ),
    ).toBeTruthy();
    // The poll's own detail names a row nobody typed and no reader can act on.
    expect(screen.queryByText(/row not visible/)).toBeNull();
  });
});
