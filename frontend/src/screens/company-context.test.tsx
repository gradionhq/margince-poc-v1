/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
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
type Me = components["schemas"]["MeResponse"];
type Grant = components["schemas"]["RbacObjectGrant"];
type SeatType = components["schemas"]["Authorization"]["seat_type"];

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

// The card scopes its write controls to what /me says the seat holds, so every
// fixture below has to answer that probe: an unanswered one denies, and the
// suite would then be driving a card with no controls at all.
function meResponse(seat: SeatType, organization: Grant): Me {
  return {
    user: {
      id: "00000000-0000-4000-8000-000000000001",
      email: "mira@acme.test",
      display_name: "Mira Voss",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
    workspace_name: "Acme",
    non_production: true,
    admin_password_link: false,
    roles: [],
    teams: [],
    authorization: { seat_type: seat, objects: { organization } },
  };
}

// A seat that may author the company: a full licence, and both anchor verbs —
// the save mints the record on its first run and replaces it afterwards, so the
// server demands `create` or `update` depending on what it finds.
const ME_EDITOR = meResponse("full", {
  create: true,
  read: true,
  update: true,
  delete: true,
});

// The `read_only` seat, exactly as the roster seeds it: it reads the company
// profile and holds no write on it, and the licence ceiling refuses a mutation
// on top of that.
const ME_READER = meResponse("read", {
  create: false,
  read: true,
  update: false,
  delete: false,
});

function backend(principal: Me = ME_EDITOR) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input instanceof Request ? input.url : input));
    const body = routeBody(url.pathname, principal);
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
}

function routeBody(path: string, principal: Me = ME_EDITOR): unknown {
  if (path === "/v1/me") {
    return principal;
  }
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
const SETTLE_MS = 10_000;

// The budget for a test that drives `renderReview`, and it must cover EVERY
// waiter in there: four run in sequence, each bounded by SETTLE_MS. A test whose
// own limit is smaller than the sum lets vitest fire while a waiter still has
// budget, and what surfaces then is an opaque timeout rather than the assertion
// the test was written to make. Vitest's per-test default is 5s, which is less
// than one of these waiters alone.
const TEST_MS = SETTLE_MS * 4;

// The same arithmetic for a test that renders the seeded card, opens the editor
// and then waits for focus to land in it: three sequential waiters — `renderAs`
// brings its own — so three budgets. Stated rather than left on vitest's
// default, which is smaller than one of them; the guard in
// `scripts/test-budget.test.ts` is what catches that, and what it asks for is a
// ceiling that covers the waiters, not waiters trimmed to fit a ceiling.
const DIALOG_MS = SETTLE_MS * 3;

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
  // The refresh verb only exists on a card the seeded form renders, and the
  // website it sends is that same form's — so the button's arrival IS the
  // seeding, and there is no second wait to race it. That was not true while
  // the website sat in an input beside the button: the button landed with the
  // profile and the input filled a tick later.
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

// The settings entry that leads here opens on the READ grant, so a read_only
// seat reaches this card: it may see the shared business context and may not
// write it. What that seat must NOT be handed is a control the server answers
// with a 403 — and the card must still say what it is, because an absent card
// would claim the installation has no company profile at all.
describe("CompanyContextCard write posture", () => {
  const READ_ONLY =
    "Read-only view — changing the company profile needs an organization write.";
  const SAVE = "Save company context";
  const REFRESH = "Refresh from website";
  // The row verb, named by the fact it changes rather than by the word "Edit":
  // seventeen rows offer seventeen of these buttons, and a suite that asked for
  // "Edit" would be asking which one.
  const EDIT_OFFER = "Edit What do you sell?";

  async function renderAs(principal: Me) {
    vi.stubGlobal("fetch", backend(principal));
    render(
      <Providers>
        <CompanyContextCard />
      </Providers>,
    );
    // The profile itself, waited on rather than assumed: every assertion below
    // is about what a LOADED card offers, and a card still fetching offers
    // nothing either way. The website is the second card's own answer for where
    // the profile is read from, and that card exists only once the form is
    // seeded — so its arrival is the profile's arrival.
    await screen.findByText(COMPANY.website ?? "", undefined, {
      timeout: SETTLE_MS,
    });
  }

  it("withholds both writes from a seat that holds none, and says so", async () => {
    await renderAs(ME_READER);

    // The read is granted, so the data is there to read — as the row's own
    // answer now rather than as the contents of an input.
    expect(screen.getByText(COMPANY.offer_summary ?? "")).toBeTruthy();
    // Stated once, at the surface, rather than annotated onto each absent
    // control.
    expect(screen.getByText(READ_ONLY)).toBeTruthy();
    expect(screen.queryByRole("button", { name: EDIT_OFFER })).toBeNull();
    expect(screen.queryByRole("button", { name: REFRESH })).toBeNull();
  });

  it("offers both writes to a seat that may author the profile", async () => {
    await renderAs(ME_EDITOR);

    // Without this arm the test above would pass on a card that renders no
    // buttons for anybody.
    expect(screen.getByRole("button", { name: EDIT_OFFER })).toBeTruthy();
    expect(screen.getByRole("button", { name: REFRESH })).toBeTruthy();
    // A reader who may write is told nothing about a posture they do not have.
    expect(screen.queryByText(READ_ONLY)).toBeNull();
  });

  // One PUT writes this profile, so there is one form and one Save — and both
  // are behind a row's verb rather than standing on the card. A row states an
  // ANSWER; what commits seventeen fields belongs with the seventeen fields.
  it(
    "keeps the save inside the form the row verb opens",
    async () => {
      await renderAs(ME_EDITOR);

      expect(screen.queryByRole("button", { name: SAVE })).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: EDIT_OFFER }));
      const dialog = await screen.findByRole("dialog", undefined, {
        timeout: SETTLE_MS,
      });

      expect(within(dialog).getByRole("button", { name: SAVE })).toBeTruthy();
      // The whole profile is in there, not just the row that was pressed: a form
      // holding three of seventeen fields would be committing the other fourteen
      // out of sight.
      expect(
        within(dialog).getByLabelText<HTMLInputElement>(
          "Registered legal name",
        ),
      ).toBeTruthy();
      // Focus lands on the fact whose verb was pressed, so a keyboard reader who
      // asked to change one field is not dropped at the top of a long form.
      await waitFor(
        () =>
          expect(document.activeElement).toBe(
            within(dialog).getByLabelText("What do you sell?"),
          ),
        { timeout: SETTLE_MS },
      );
    },
    DIALOG_MS,
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
