/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, type Mock, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { AssistantPanel } from "./assistant";
import { SuggestionsSection } from "./company360";
import { listFetchLimit } from "./listquery";
import {
  CompaniesScreen,
  CompanyScreen,
  companyEditFields,
  mapOrgUpdate,
} from "./organizations";

// Company-360 enrichment (EP05 scrapeCompany): one click stages a 🟡
// evidence-backed proposal — human field labels, per-field confidence +
// evidence, the confirm-first banner (nothing written until the inbox
// accept), and honest 422 degradation with the server's detail verbatim.
//
// Below that: the same P-14/15/16/1 shared-block wiring as contacts
// (people.test.tsx) — search/sort/pagination, the rich create modal
// (display_name/legal_name/industry/size_band/domains), the company-360
// If-Match edit, and the duplicate_domain dedupe link.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// The record's rare verbs — edit, merge, archive, share, full history — live
// behind the header's overflow menu, so a test that operates one opens the
// menu first. Returns once the item is on screen.
//
// getByTestId would find the items whether the menu were open or shut: they
// stay mounted so their dialogs survive the click that closes the menu. The
// closed state is asserted separately, on the `hidden` panel.
async function openRecordMenu(testId: string): Promise<HTMLElement> {
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "More actions" })).toBeTruthy(),
  );
  await userEvent.click(screen.getByRole("button", { name: "More actions" }));
  await waitFor(() => expect(screen.getByTestId(testId)).toBeTruthy());
  return screen.getByTestId(testId);
}

const org = {
  id: "o-1",
  display_name: "Brandt Automotive GmbH",
  industry: "Automotive",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

// The 360 backstop: an assembled account with every section present and
// empty. The pre-existing suites below exercise the enrich/deep-read/profile
// cards, not the composite read, and an assembled-but-empty page is the
// state that lets them render without asserting anything about it.
const org360 = {
  as_of: "2026-06-01T09:00:00Z",
  organization: org,
  sections_omitted: [],
  people: { data: [], page: { has_more: false, next_cursor: null } },
  deals: {
    data: [],
    page: { has_more: false, next_cursor: null },
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    lost_count: 0,
  },
  activities: { data: [], page: { has_more: false, next_cursor: null } },
  next_steps: { data: [], page: { has_more: false, next_cursor: null } },
  pending_approvals: { data: [], page: { has_more: false, next_cursor: null } },
  tags: [],
  list_memberships: [],
  since_last_visit: {
    baseline_at: null,
    new_activities: 0,
    deal_stage_moves: 0,
    pending_proposals: 0,
  },
  // Assembled and empty: the section came back, and the account needs nothing.
  // Suites that exercise the card pass their own through `org360`.
  suggestions: [],
  suggestions_dropped: 0,
};

// The roll-up backstop. It sits in the company view's left rail now rather
// than behind a tab, so every test that renders the page fires this GET.
const emptyRollup = {
  root_id: "o-1",
  scope: "tree",
  weighted_pipeline: { amount_minor: 0, currency: "EUR" },
  closed_won: { amount_minor: 0, currency: "EUR" },
  activity_count_30d: 0,
  aggregated_account_count: 1,
  restricted_excluded: [],
  computed_at: "2026-06-01T09:00:00Z",
};

// The brief backstop: a deterministic brief with nothing to say. The suites
// below exercise other cards, and a brief that renders in its quietest real
// state keeps them free of it.
const emptyBrief = {
  organization_id: "o-1",
  generated_at: "2026-06-01T09:00:00Z",
  generated_by: "deterministic",
  sentences: [],
};

// The dormant/no-interactions strength response — the default backstop for
// every fetch stub below that isn't itself exercising the strength card
// (P-4): the Company Overview now fires this GET unconditionally, and none
// of the pre-existing tests below care about its shape.
const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

const proposal = {
  proposal_id: "pr-1",
  organization_id: "o-1",
  source_url: "https://brandt.example",
  status: "staged",
  fields: [
    {
      field: "value_proposition",
      value: "Fleet retrofits without downtime",
      evidence_snippet: "We retrofit fleets without downtime",
      source_url: "https://brandt.example",
      confidence: 0.85,
    },
  ],
};

function stubApi(enrich: () => Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      if (method === "POST" && url.pathname.endsWith("/enrich")) {
        return enrich();
      }
      if (url.pathname.endsWith("/strength")) {
        return jsonResponse(dormantStrength);
      }
      if (url.pathname.endsWith("/context")) {
        return jsonResponse({
          anchor: { type: "organization", id: "o-1" },
          sections: [],
        });
      }
      if (url.pathname.endsWith("/organizations/o-1/360")) {
        return jsonResponse(org360);
      }
      if (url.pathname.endsWith("/hierarchy-rollup")) {
        return jsonResponse(emptyRollup);
      }
      if (url.pathname.endsWith("/brief")) {
        return jsonResponse(emptyBrief);
      }
      if (url.pathname.endsWith("/organizations/o-1")) {
        return jsonResponse(org);
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }),
  );
}

// openHistory switches to the tab the account's chronology now lives on. The
// timeline left the overview when the page gained People and History tabs, so
// a test about the timeline has to go there first.
async function openHistory() {
  await userEvent.click(await screen.findByRole("button", { name: "History" }));
}

// openProfile switches to the tab that carries the account's own reference
// material — the dossier, its filed fields, its facts, who it is connected to
// and the one-off tools (enrichment, the deep read, the hierarchy roll-up).
// All of it used to render under every tab; it is Profile-only now.
async function openProfile() {
  await userEvent.click(await screen.findByRole("button", { name: "Profile" }));
}

describe("company-360 enrichment", () => {
  it("stages an evidence-backed proposal: human labels, confidence, confirm-first banner", async () => {
    stubApi(() => jsonResponse(proposal));
    render(<CompanyScreen id="o-1" />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    await openProfile();
    await userEvent.click(screen.getByRole("button", { name: "Read now" }));
    await waitFor(() =>
      expect(screen.getByText("Value proposition")).toBeTruthy(),
    );
    expect(screen.queryByText("value_proposition")).toBeNull();
    expect(screen.getByText("Fleet retrofits without downtime")).toBeTruthy();
    expect(screen.getByText(/Staged — nothing written yet/)).toBeTruthy();
    expect(screen.getByText(/read from https:\/\/brandt.example/)).toBeTruthy();
  });

  it("renders the honest 422 detail when the page is unreadable", async () => {
    stubApi(() =>
      jsonResponse(
        {
          title: "Unprocessable",
          detail: "the organization has no domain to read",
        },
        422,
      ),
    );
    render(<CompanyScreen id="o-1" />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    await openProfile();
    await userEvent.click(screen.getByRole("button", { name: "Read now" }));
    await waitFor(() =>
      expect(
        screen.getByText("the organization has no domain to read"),
      ).toBeTruthy(),
    );
  });
});

// The deep read (A102/R2): one click starts a background whole-site crawl and
// the card polls the read report until it lands on a terminal status. The
// report is the transparency surface — a partial crawl must SAY it stopped
// early and name every skipped page's reason, and staged proposals point at
// the approvals inbox.
const runningRead = {
  read_id: "rd-1",
  organization_id: "o-1",
  seed_url: "https://brandt.example",
  status: "running",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  pages: [
    { url: "https://brandt.example/", kind: "home" },
    { url: "https://brandt.example/team", kind: "team" },
  ],
  skipped: [],
  proposal_ids: [],
  created_at: "2026-07-17T08:00:00Z",
};

function stubDeepRead(options: {
  post?: () => Response;
  report?: () => Response;
}) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      calls.push(`${method} ${url.pathname}`);
      if (method === "POST" && url.pathname.endsWith("/deep-read")) {
        return (
          options.post ??
          (() => jsonResponse({ read_id: "rd-1", status: "queued" }, 202))
        )();
      }
      if (url.pathname.includes("/site-reads/")) {
        return (options.report ?? (() => jsonResponse(runningRead)))();
      }
      if (url.pathname.endsWith("/strength")) {
        return jsonResponse(dormantStrength);
      }
      if (url.pathname.endsWith("/context")) {
        return jsonResponse({
          anchor: { type: "organization", id: "o-1" },
          sections: [],
        });
      }
      if (url.pathname.endsWith("/organizations/o-1/360")) {
        return jsonResponse(org360);
      }
      if (url.pathname.endsWith("/hierarchy-rollup")) {
        return jsonResponse(emptyRollup);
      }
      if (url.pathname.endsWith("/brief")) {
        return jsonResponse(emptyBrief);
      }
      if (url.pathname.endsWith("/organizations/o-1")) {
        return jsonResponse(org);
      }
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }),
  );
  return { calls };
}

async function startDeepRead(calls: string[]) {
  await waitFor(() =>
    expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
  );
  await openProfile();
  await userEvent.click(screen.getByRole("button", { name: "Read full site" }));
  await waitFor(() =>
    expect(
      calls.some(
        (call) =>
          call.startsWith("POST") &&
          call.endsWith("/organizations/o-1/deep-read"),
      ),
    ).toBe(true),
  );
}

describe("company-360 deep read", () => {
  it("POSTs deep-read on click and polls the read report every 3s while running", async () => {
    const { calls } = stubDeepRead({});
    const reportCalls = () =>
      calls.filter((call) =>
        call.endsWith("/organizations/o-1/site-reads/rd-1"),
      ).length;
    // The whole flow runs on fake timers so react-query's 3s poll interval is
    // scheduled on the fake clock (a poll timer armed on the real clock could
    // not be advanced). Each advance flushes due timers plus the microtask
    // chains behind the stubbed fetches.
    const flush = () =>
      act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
    vi.useFakeTimers();
    try {
      render(<CompanyScreen id="o-1" />);
      await flush();
      await flush();
      fireEvent.click(screen.getByRole("button", { name: "Profile" }));
      await flush();
      fireEvent.click(screen.getByRole("button", { name: "Read full site" }));
      await flush();
      await flush();
      expect(
        calls.some(
          (call) =>
            call.startsWith("POST") &&
            call.endsWith("/organizations/o-1/deep-read"),
        ),
      ).toBe(true);
      // A running report renders pages-so-far progress…
      expect(reportCalls()).toBe(1);
      expect(screen.getByText("2 pages read so far")).toBeTruthy();
      // …and keeps polling: the 3s interval fires another report fetch.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(reportCalls()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a budget deferral as an automatic resume, not a failed read", async () => {
    const { calls } = stubDeepRead({
      report: () =>
        jsonResponse({
          ...runningRead,
          status: "deferred",
          status_code: "budget_deferred",
          status_detail:
            "AI budget reached its current limit. This website read will resume automatically.",
          next_attempt_at: "2026-08-01T00:00:00Z",
        }),
    });
    render(<CompanyScreen id="o-1" />);
    await startDeepRead(calls);

    await waitFor(() =>
      expect(screen.getByText("Waiting for AI budget")).toBeTruthy(),
    );
    expect(
      screen.getByText(/This website read will resume automatically/),
    ).toBeTruthy();
    expect(screen.getByText(/Resumes automatically/)).toBeTruthy();
    expect(screen.queryByText("Failed")).toBeNull();
  });

  it("a partial report says it stopped early and names every skip reason", async () => {
    const { calls } = stubDeepRead({
      report: () =>
        jsonResponse({
          ...runningRead,
          status: "partial",
          stopped_reason: "page_cap",
          fact_count: 6,
          skipped: [
            { url: "https://brandt.example/careers", reason: "robots" },
            { url: "https://elsewhere.example/profile", reason: "off_domain" },
          ],
          finished_at: "2026-07-17T08:04:00Z",
        }),
    });
    render(<CompanyScreen id="o-1" />);
    await startDeepRead(calls);

    await waitFor(() =>
      expect(screen.getByText("Stopped early: page cap")).toBeTruthy(),
    );
    expect(screen.getByText("6 evidenced facts staged")).toBeTruthy();
    expect(screen.getByText("Pages skipped")).toBeTruthy();
    expect(screen.getByText("robots.txt")).toBeTruthy();
    expect(screen.getByText("off domain")).toBeTruthy();
    expect(screen.getByText("brandt.example/careers")).toBeTruthy();
  });

  it("a done report lists the pages read and links staged proposals to the inbox", async () => {
    const { calls } = stubDeepRead({
      report: () =>
        jsonResponse({
          ...runningRead,
          status: "done",
          fact_count: 9,
          proposal_ids: ["ap-1", "ap-2"],
          finished_at: "2026-07-17T08:05:00Z",
        }),
    });
    render(<CompanyScreen id="o-1" />);
    await startDeepRead(calls);

    await waitFor(() =>
      expect(
        screen.getByText("2 proposals waiting for your review"),
      ).toBeTruthy(),
    );
    // A complete crawl carries no stopped-early banner.
    expect(screen.queryByText(/Stopped early:/)).toBeNull();
    expect(screen.getByText("Pages read")).toBeTruthy();
    expect(screen.getByText("Home")).toBeTruthy();
    expect(screen.getByText("brandt.example/team")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Open inbox" }));
    expect(window.location.hash).toBe("#/inbox");
  });

  it("renders the honest 422 detail when the org has no website on file", async () => {
    stubDeepRead({
      post: () =>
        jsonResponse(
          { title: "Unprocessable", detail: "no website on file" },
          422,
        ),
    });
    render(<CompanyScreen id="o-1" />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    await openProfile();
    await userEvent.click(
      screen.getByRole("button", { name: "Read full site" }),
    );
    await waitFor(() =>
      expect(screen.getByText("no website on file")).toBeTruthy(),
    );
  });

  it("names the unwired seam on a 501 instead of a generic failure", async () => {
    stubDeepRead({
      post: () => jsonResponse({ title: "Not Implemented" }, 501),
    });
    render(<CompanyScreen id="o-1" />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    await openProfile();
    await userEvent.click(
      screen.getByRole("button", { name: "Read full site" }),
    );
    await waitFor(() =>
      expect(
        screen.getByText("Site reading is not configured on this server."),
      ).toBeTruthy(),
    );
  });
});

// A URL-capturing fetch stub shared across the P-14/15/16 wiring tests
// below: every request is recorded so a test can assert the params it
// carried, and a caller-supplied responder decides what comes back. Strength
// requests are answered with the dormant default up front (overridable via
// `strength`) so tests that don't care about relationship strength don't have
// to plumb a branch for it.
function stubFetch(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response>,
  options?: Readonly<{
    strength?: unknown;
    org360?: unknown;
    // A roll-up body, or a ready Response when the suite is exercising a
    // refusal (the 422 FX case) rather than a payload.
    rollup?: unknown | Response;
    brief?: unknown;
  }>,
): {
  fetchMock: Mock<(request: Request) => Promise<Response>>;
  urls: string[];
} {
  const urls: string[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    urls.push(request.url);
    const pathname = new URL(request.url).pathname;
    if (pathname.endsWith("/strength")) {
      return jsonResponse(options?.strength ?? dormantStrength);
    }
    if (pathname.endsWith("/context")) {
      return jsonResponse({
        anchor: { type: "organization", id: "o-1" },
        sections: [],
      });
    }
    // The company view fires both of these on every render — the composite
    // read that serves the page, and the roll-up that now sits in its left
    // rail rather than behind a tab. They default to an assembled-but-empty
    // account so a suite testing some other card renders at all; a suite
    // that IS testing one of them passes its own through options, the same
    // shape `strength` already uses.
    if (pathname.endsWith("/360")) {
      return jsonResponse(options?.org360 ?? org360);
    }
    if (pathname.endsWith("/hierarchy-rollup")) {
      return rollupResponse(options?.rollup);
    }
    if (pathname.endsWith("/brief")) {
      return jsonResponse(options?.brief ?? emptyBrief);
    }
    return responder(request.url, request.method, request);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, urls };
}

// rollupResponse lets a suite hand back either a body or a whole Response,
// because one of them asserts the honest 422 rather than a payload.
function rollupResponse(rollup: unknown): Response {
  if (rollup instanceof Response) {
    return rollup;
  }
  return jsonResponse(rollup ?? emptyRollup);
}

function emptyPage() {
  return jsonResponse({
    data: [],
    page: { next_cursor: null, has_more: false },
  });
}

describe("CompaniesScreen — search/sort/pagination (P-14)", () => {
  it("carries the debounced search term into the next fetch", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<CompaniesScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByPlaceholderText("Search"), {
        target: { value: "brandt" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() =>
      expect(urls.some((url) => url.includes("q=brandt"))).toBe(true),
    );
  });

  it("fetches the next cursor page when the pager steps past the loaded page", async () => {
    const { urls } = stubFetch(async (url) => {
      if (url.includes("cursor=c1")) {
        return jsonResponse({
          data: [{ ...org, id: "o-2", display_name: "Nordwind Logistik" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        data: [org],
        page: { next_cursor: "c1", has_more: true },
      });
    });
    render(<CompaniesScreen />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );

    const next = screen.getByRole("button", { name: "Next ›" });
    expect((next as HTMLButtonElement).disabled).toBe(false);
    await userEvent.click(next);

    await waitFor(() =>
      expect(screen.getByText("Nordwind Logistik")).toBeTruthy(),
    );
    expect(urls.some((url) => url.includes("cursor=c1"))).toBe(true);
  });
});

describe("CompaniesScreen — what an account is to us", () => {
  it("names every relationship type the account carries", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [
          {
            ...org,
            lifecycle: "prospect",
            relationship_types: ["partner", "supplier"],
          },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );

    // Both, not the first: the field is multi-valued because an account really
    // can be two things at once, and showing one would make the other look
    // untrue. The filter offers these values, so the column has to read them
    // back — a narrowed list that cannot say why a row matched is a list the
    // reader has to take on faith.
    expect(screen.getByText("Partner")).toBeTruthy();
    expect(screen.getByText("Supplier")).toBeTruthy();
    // And the two columns stay distinct: an account can be a Partner sitting
    // at Prospect.
    expect(screen.getByText("Prospect")).toBeTruthy();
  });

  it("leaves the cell empty when the account carries none", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [{ ...org, relationship_types: [] }],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    expect(screen.queryByText("Partner")).toBeNull();
  });
});

describe("CompaniesScreen — how many work here, how many deals are open", () => {
  it("shows both counts, zero included", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [
          { ...org, contact_count: 4, open_deal_count: 2 },
          {
            ...org,
            id: "o-2",
            display_name: "Quiet Ltd",
            contact_count: 0,
            open_deal_count: 0,
          },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    const first = (await screen.findByText("Brandt Automotive GmbH")).closest(
      "tr",
    );
    const quiet = screen.getByText("Quiet Ltd").closest("tr");
    if (!first || !quiet) {
      throw new Error("rows must render inside table rows");
    }
    expect(within(first).getByText("4")).toBeTruthy();
    expect(within(first).getByText("2")).toBeTruthy();
    // Zero is a number: "no contacts" and "no open deals" are facts, and a
    // blank cell would read as "not shown".
    expect(within(quiet).getAllByText("0").length).toBe(2);
  });

  it("leaves the deal count blank when the server withheld it", async () => {
    // A role without computed_field:read gets no key at all (STATE-4), and
    // the column must not turn that absence into a confident 0.
    stubFetch(async () =>
      jsonResponse({
        data: [{ ...org, contact_count: 3 }],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    const row = (await screen.findByText("Brandt Automotive GmbH")).closest(
      "tr",
    );
    if (!row) {
      throw new Error("row must render inside a table row");
    }
    expect(within(row).getByText("3")).toBeTruthy();
    expect(within(row).queryByText("0")).toBeNull();
  });
});

describe("CompaniesScreen — list dials reach the server (P-14)", () => {
  it("asks the server to re-sort instead of reordering the rows it holds", async () => {
    const user = userEvent.setup();
    const { urls } = stubFetch(async () => emptyPage());
    render(<CompaniesScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    await user.click(screen.getByRole("button", { name: "Sort by Company" }));

    // The rows the browser holds are one page of a keyset walk, so a sort that
    // only reordered them would rank a slice and present it as the whole list.
    await waitFor(() =>
      expect(
        urls.some(
          (url) =>
            url.includes("sort=display_name") && !url.includes("cursor="),
        ),
      ).toBe(true),
    );
  });

  it("sorts by last activity on the server, not over the rows it holds", async () => {
    const user = userEvent.setup();
    const { urls } = stubFetch(async () =>
      jsonResponse({
        data: [{ ...org, last_activity_at: "2026-08-10T12:00:00Z" }],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );

    await user.click(
      screen.getByRole("button", { name: "Sort by Last activity" }),
    );
    // The server owns the order: a fresh keyset walk under the new sort.
    await waitFor(() =>
      expect(
        urls.some(
          (url) =>
            url.includes("sort=last_activity_at") && !url.includes("cursor="),
        ),
      ).toBe(true),
    );
  });

  it("narrows to one lifecycle stage through the server", async () => {
    const user = userEvent.setup();
    const { urls } = stubFetch(async () => emptyPage());
    render(<CompaniesScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    await user.click(screen.getByRole("button", { name: "Customers" }));

    await waitFor(() =>
      expect(urls.some((url) => url.includes("lifecycle=customer"))).toBe(true),
    );
  });

  it("reads whole rendered pages at whatever size the reader picked", async () => {
    const user = userEvent.setup();
    const { urls } = stubFetch(async () => emptyPage());
    render(<CompaniesScreen />);
    // One read carries several rendered pages, so the limit is a multiple of
    // the footer's size and never the size itself — that is what lets the
    // pager offer page 2 before the reader has asked for it.
    await waitFor(() =>
      expect(
        urls.some((url) => url.includes(`limit=${listFetchLimit(25)}`)),
      ).toBe(true),
    );

    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Rows per page" }),
      "50 per page",
    );

    // The read follows the reader's size rather than a literal. It did not:
    // the screen asked for 50 and drew 25, and said so — "1-25 of 50 loaded
    // so far".
    const resized = `limit=${listFetchLimit(50)}`;
    await waitFor(() =>
      expect(urls.some((url) => url.includes(resized))).toBe(true),
    );

    // A new page size restarts the keyset walk. Carrying the old cursor over
    // would continue a walk taken at the previous size, so the reader would
    // resume mid-list while believing they were on page one.
    expect(
      urls
        .filter((url) => url.includes(resized))
        .every((url) => !url.includes("cursor=")),
    ).toBe(true);
  });
});

describe("CompaniesScreen — rich create (P-15)", () => {
  it("posts display_name + size_band + domains + source:manual on submit", async () => {
    const user = userEvent.setup();
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/organizations")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...org, id: "o-new" }, 201);
      }
      return emptyPage();
    });
    render(<CompaniesScreen />);
    await user.click(screen.getByTestId("new-record"));
    await user.type(
      screen.getByLabelText("Company name *"),
      "Otto Fischer GmbH",
    );
    await pickOption(user, screen.getByLabelText("Company size"), "11-50");
    await user.click(screen.getByText("Add domain"));
    await user.type(screen.getByLabelText("Domain *"), "otto.example");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      display_name: "Otto Fischer GmbH",
      size_band: "11-50",
      source: "manual",
      domains: [{ domain: "otto.example", is_primary: false }],
    });
  });
});

describe("CompanyScreen — edit with If-Match (P-1)", () => {
  it("PATCHes /organizations/{id} with If-Match:<version> and only update fields", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "PATCH") {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...org, industry: "Manufacturing", version: 2 });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("edit-record"));
    const industry = await screen.findByLabelText("Industry");
    await userEvent.clear(industry);
    await userEvent.type(industry, "Manufacturing");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ industry: "Manufacturing" });
    // The org fixture carries no domains, so an edit that doesn't touch them
    // omits the field (untouched) rather than clearing the stored set.
    expect(patchBody).not.toHaveProperty("domains");
  });

  // relationship_types is a REPLACE-SET, so the edit modal must prefill it:
  // an unseeded multiselect collects as the empty string, which is the honest
  // empty set, and saving an unrelated field would retire every type the
  // account has without the reader touching them.
  it("preserves the account's relationship types when an unrelated field is edited", async () => {
    let patchBody: unknown = null;
    const partner = {
      ...org,
      lifecycle: "customer",
      relationship_types: ["partner", "supplier"],
    };
    stubFetch(async (url, method, request) => {
      if (method === "PATCH") {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({
          ...partner,
          industry: "Manufacturing",
          version: 2,
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(partner);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("edit-record"));
    const industry = await screen.findByLabelText("Industry");
    await userEvent.clear(industry);
    await userEvent.type(industry, "Manufacturing");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({
      industry: "Manufacturing",
      relationship_types: ["partner", "supplier"],
    });
  });
});

// B7: the edit modal's repeatable domains field replace-sets the org's live
// domains on PATCH. Adding a row from the modal and saving carries a
// `domains[]` body — the fork-owned editable seam over the firmographics card.
describe("CompanyScreen — edit domains round-trip (B7)", () => {
  it("PATCHes domains[] when a domain is added in the edit modal", async () => {
    let patchBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "PATCH") {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...org, version: 2 });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("edit-record"));
    await screen.findByLabelText("Industry");
    await userEvent.click(screen.getByText("Add domain"));
    await userEvent.type(screen.getByLabelText("Domain *"), "brandt.example");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({
      domains: [{ domain: "brandt.example", is_primary: false }],
    });
  });
});

// B7 unit: the PATCH mapping sends `domains` only when the set actually
// changed — untouched edits stay sparse (omit), and removing every row sends
// the empty replace-set (`[]` = clear all), the two cases the API distinguishes.
describe("mapOrgUpdate — domains change detection (P1)", () => {
  const dom = (domain: string, isPrimary: boolean) => ({
    id: "00000000-0000-0000-0000-000000000000",
    domain,
    is_primary: isPrimary,
    source: "manual",
    captured_by: "human:x",
  });

  it("omits domains when the set is unchanged", () => {
    const body = mapOrgUpdate(
      { display_name: "X" },
      { domains: [{ domain: "a.test", is_primary: "true" }] },
      [dom("a.test", true)],
    );
    expect(body).not.toHaveProperty("domains");
  });

  it("sends [] when every domain row is removed (clear all)", () => {
    const body = mapOrgUpdate({ display_name: "X" }, { domains: [] }, [
      dom("a.test", true),
    ]);
    expect(body.domains).toEqual([]);
  });

  it("sends the new set when a domain is added", () => {
    const body = mapOrgUpdate(
      { display_name: "X" },
      {
        domains: [
          { domain: "a.test", is_primary: "true" },
          { domain: "b.test", is_primary: "" },
        ],
      },
      [dom("a.test", true)],
    );
    expect(body.domains).toEqual([
      { domain: "a.test", is_primary: true },
      { domain: "b.test", is_primary: false },
    ]);
  });
});

// B5: the Firmographics & legal card renders the org's confirmed profile
// fields evidence-or-omit — a returned field shows with its human label and
// value, a field the read never grounded is simply absent, and an empty read
// states so honestly instead of inventing rows.
describe("CompanyScreen — profile fields card (B5)", () => {
  it("renders a confirmed field's label + value and omits absent fields", async () => {
    stubFetch(async (url) => {
      if (url.includes("/profile-fields")) {
        return jsonResponse({
          data: [
            {
              field: "value_proposition",
              value: "Fleet retrofits without downtime",
              source: "site_read",
              captured_by: "agent:capture",
              evidence_snippet: "We retrofit fleets without downtime",
              source_url: "https://brandt.example",
              confidence: 0.9,
              updated_at: "2026-07-01T00:00:00Z",
            },
          ],
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() =>
      expect(screen.getByText("What they promise")).toBeTruthy(),
    );
    expect(screen.getByText("Fleet retrofits without downtime")).toBeTruthy();
    // The value carries ONE affordance now: an evidence mark on the value
    // itself. The old footer — a provenance tag, a confidence meter and an
    // evidence chip stacked under every row — is gone, because three chips
    // under a value read as clutter rather than as "this was derived".
    const marked = screen.getByRole("button", {
      name: /Fleet retrofits without downtime/,
    });
    expect(marked.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/^High$|^Medium$|^Low$/)).toBeNull();
    // A field the read never grounded (legal name) must not be invented.
    expect(screen.queryByText("Registered legal name")).toBeNull();
    // The empty-state copy only shows when nothing was read.
    expect(screen.queryByText(/Nothing read yet/)).toBeNull();
  });

  it("shows the honest empty state when nothing has been read yet", async () => {
    stubFetch(async (url) => {
      if (url.includes("/profile-fields")) {
        return jsonResponse({ data: [] });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() =>
      expect(screen.getByText(/Nothing read yet/)).toBeTruthy(),
    );
  });
});

// B6: the facts card groups site-read facts into the four fixed categories,
// omits empty categories, and renders each fact's field → value row.
describe("CompanyScreen — facts card (B6)", () => {
  it("groups facts by category and omits empty categories", async () => {
    stubFetch(async (url) => {
      if (url.endsWith("/facts")) {
        return jsonResponse({
          data: [
            {
              category: "market",
              field: "served_industry",
              value: "Automotive OEMs",
              value_key: "served_industry:automotive-oems",
              source: "site_read",
              captured_by: "agent:capture",
              updated_at: "2026-07-01T00:00:00Z",
            },
            {
              category: "company",
              field: "founded_year",
              value: "1998",
              value_key: "founded_year:1998",
              source: "site_read",
              captured_by: "agent:capture",
              updated_at: "2026-07-01T00:00:00Z",
            },
            {
              category: "offering",
              field: "service",
              value: "Fleet retrofits",
              value_key: "service:fleet-retrofits",
              source: "site_read",
              captured_by: "agent:capture",
              updated_at: "2026-07-01T00:00:00Z",
            },
          ],
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() =>
      expect(screen.getByText("Facts read from the site")).toBeTruthy(),
    );
    // Scoped to the facts card: the right rail carries a Signals card of its
    // own, and "which categories did the site read produce" is a question
    // about this card, not about the page.
    const factsCard = screen
      .getByText("Facts read from the site")
      .closest("section");
    if (!factsCard) {
      throw new Error("the facts card has no section wrapper");
    }
    const facts = within(factsCard);
    expect(facts.getByText("Company")).toBeTruthy();
    expect(facts.getByText("Offering")).toBeTruthy();
    expect(facts.getByText("Market")).toBeTruthy();
    expect(facts.getByText("1998")).toBeTruthy();
    expect(facts.getByText("Automotive OEMs")).toBeTruthy();
    expect(facts.getByText("Fleet retrofits")).toBeTruthy();
    // No signal fact was returned, so that subsection is absent.
    expect(facts.queryByText("Signals")).toBeNull();
  });
});

describe("CompanyScreen — archive (P-3)", () => {
  it("opens a confirm, DELETEs /organizations/{id} on confirm, and navigates to the list", async () => {
    let deleted = false;
    stubFetch(async (url, method) => {
      if (method === "DELETE" && url.includes("/organizations/o-1")) {
        deleted = true;
        return jsonResponse({ ...org, archived_at: "2026-07-13T00:00:00Z" });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("archive-record"));
    await userEvent.click(screen.getByTestId("archive-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
    expect(window.location.hash).toBe("#/companies");
  });
});

describe("CompanyScreen — overlay mode write affordances", () => {
  // The mirror's own write-back seam serves update and archive for an
  // organization (overlay/provider_writes.go SupportsWrite), so both render
  // here; merge has no incumbent-first projection and stays refused, so it
  // stays hidden.
  function meResponse() {
    return jsonResponse({
      user: { id: "u1", email: "me@brandt.example", locale: "en-US" },
      roles: ["admin"],
      teams: [],
      system_of_record: { mode: "overlay" },
    });
  }

  it("serves Edit and Archive, hides Merge", async () => {
    stubFetch(async (url, method) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      if (method === "PATCH") {
        return jsonResponse(org);
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await openRecordMenu("edit-record");
    expect(screen.getByTestId("archive-record")).toBeTruthy();
    // Anchor on something only overlay mode produces, so the absence below
    // is asserted AFTER /me landed. Waiting on the absence alone passes on
    // the first tick and would still pass if Merge were rendered in overlay.
    await waitFor(() =>
      expect(screen.getByText(/not assembled here/)).toBeTruthy(),
    );
    expect(screen.queryByTestId("merge-record")).toBeNull();
  });

  it("Edit's real click path PATCHes and the 360 shows the saved industry", async () => {
    // Mutable so the refetch after a successful save (useUpdateRecord
    // invalidates the record query) reflects the write, not a stale echo —
    // the same "mirror re-read reflects write-back" shape
    // overlay.Provider.Update gives via mirrorWriteResult.
    let current = org;
    stubFetch(async (url, method, request) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      if (method === "PATCH") {
        const body = JSON.parse(await request.text());
        current = { ...current, ...body };
        return jsonResponse(current);
      }
      return jsonResponse(current);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("edit-record"));
    const industry = await screen.findByLabelText("Industry");
    await userEvent.clear(industry);
    await userEvent.type(industry, "Manufacturing");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Manufacturing")).toBeTruthy();
  });

  it("names the partial write-back in the edit form", async () => {
    stubFetch(async (url) => {
      if (url.includes("/me")) {
        return meResponse();
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("edit-record"));
    expect(
      screen.getByText(/Only the fields HubSpot accepts are written back/),
    ).toBeTruthy();
  });
});

describe("CompaniesScreen — archived marking (P-3)", () => {
  it("shows an Archived badge on a row with archived_at set", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [{ ...org, archived_at: "2026-07-01T00:00:00Z" }],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<CompaniesScreen />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    expect(screen.getByText("Archived")).toBeTruthy();
  });
});

describe("CompaniesScreen — dedupe view-existing link (P-16)", () => {
  it("renders a link to the collided record on a duplicate_domain 409", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.includes("/organizations")) {
        return jsonResponse(
          {
            type: "about:blank",
            title: "Conflict",
            detail: "domain already in use",
            code: "duplicate_domain",
            details: { existing_id: "01X" },
          },
          409,
        );
      }
      return emptyPage();
    });
    render(<CompaniesScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(
      screen.getByLabelText("Company name *"),
      "Dup Company",
    );
    await userEvent.click(screen.getByText("Add domain"));
    await userEvent.type(screen.getByLabelText("Domain *"), "dup.example");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.getByText("View existing record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("View existing record"));
    expect(window.location.hash).toBe("#/companies/01X");
  });
});

describe("CompanyScreen — merge into target (P-2)", () => {
  const acme = { ...org, id: "o-2", display_name: "Acme Corp" };

  it("searches, excludes the source row, and merges into the picked target", async () => {
    let mergeBody: unknown = null;
    let mergeHeader: string | null = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/organizations/o-1/merge")) {
        mergeHeader = request.headers.get("If-Match");
        mergeBody = JSON.parse(await request.text());
        return jsonResponse({ ...acme, version: 2 });
      }
      if (url.includes("/organizations?") && url.includes("q=acme")) {
        return jsonResponse({
          data: [org, acme],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("merge-record"));
    await userEvent.type(screen.getByPlaceholderText("Search…"), "acme");

    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    const dialog = screen.getByRole("dialog");
    await waitFor(() =>
      expect(within(dialog).getByText("Acme Corp")).toBeTruthy(),
    );
    // The source row must never appear as a mergeable target.
    expect(within(dialog).queryByText("Brandt Automotive GmbH")).toBeNull();

    await userEvent.click(within(dialog).getByText("Acme Corp"));
    await userEvent.click(screen.getByTestId("merge-confirm"));

    await waitFor(() => expect(mergeBody).toBeTruthy());
    expect(mergeBody).toMatchObject({ target_id: "o-2" });
    expect(mergeHeader).toBe("1");
    expect(window.location.hash).toBe("#/companies/o-2");
  });
});

const employmentRel = {
  id: "rel-1",
  kind: "employment",
  person_id: "p-1",
  organization_id: "o-1",
  role: "cto",
  is_current_primary: true,
  started_at: "2024-01-01",
  ended_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("CompanyScreen — Relationships tab (P-5)", () => {
  it("shows an Overview/Relationships tab bar and lists relationships by organization_id", async () => {
    stubFetch(async (url) => {
      if (
        url.includes("/relationships") &&
        url.includes("organization_id=o-1")
      ) {
        return jsonResponse({
          data: [employmentRel],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() => expect(screen.getByText("Employment")).toBeTruthy());
    expect(screen.getByText("cto")).toBeTruthy();
    expect(screen.getByText("p-1")).toBeTruthy();
  });

  it("adding a relationship from the company side POSTs organization_id + the picked person_id", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/relationships")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...employmentRel, id: "rel-new" }, 201);
      }
      if (
        url.includes("/relationships") &&
        url.includes("organization_id=o-1")
      ) {
        return emptyPage();
      }
      if (url.includes("/people?") && url.includes("q=anna")) {
        return jsonResponse({
          data: [{ id: "p-1", full_name: "Anna Weber" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openProfile();
    await waitFor(() =>
      expect(screen.getByTestId("add-relationship")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("add-relationship"));

    await userEvent.type(screen.getByPlaceholderText("Search…"), "anna");
    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() => expect(screen.getByText("Anna Weber")).toBeTruthy());
    await userEvent.click(screen.getByText("Anna Weber"));
    await userEvent.click(screen.getByTestId("add-relationship-submit"));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      organization_id: "o-1",
      person_id: "p-1",
      kind: "employment",
      source: "manual",
    });
  });
});

const rollup = {
  root_id: "o-1",
  scope: "tree",
  weighted_pipeline: { amount_minor: 4_800_000, currency: "EUR" },
  closed_won: { amount_minor: 1_200_000, currency: "EUR" },
  activity_count_30d: 12,
  aggregated_account_count: 3,
  restricted_excluded: [],
  computed_at: "2026-07-01T09:30:00Z",
};

describe("CompanyScreen — hierarchy roll-up in the rail (P-7)", () => {
  it("shows the weighted pipeline, closed-won, activity, and account figures", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(org);
      },
      { rollup },
    );
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() => expect(screen.getByText("€48,000.00")).toBeTruthy());
    expect(screen.getByText("€12,000.00")).toBeTruthy();
    expect(screen.getByText("12")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
  });

  it("renders the honest FX-unavailable message instead of zeros on a 422", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(org);
      },
      {
        rollup: jsonResponse(
          { title: "Unprocessable", code: "fx_rate_unavailable" },
          422,
        ),
      },
    );
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() =>
      expect(
        screen.getByText(
          "A currency conversion rate is missing — the roll-up cannot be computed.",
        ),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("€0.00")).toBeNull();
  });

  it("discloses accounts excluded because the viewer cannot read them", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(org);
      },
      {
        rollup: {
          ...rollup,
          restricted_excluded: [
            { id: "o-9", display_name: "Hidden Subsidiary GmbH" },
          ],
        },
      },
    );
    render(<CompanyScreen id="o-1" />);
    await openProfile();

    await waitFor(() =>
      expect(
        screen.getByText("1 account(s) not visible to you were excluded"),
      ).toBeTruthy(),
    );
  });
});

describe("CompanyScreen — the account pulse line (P-4)", () => {
  it("names the way in and when they last spoke, and shows no composite score", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        if (url.includes("/people/p-1")) {
          return jsonResponse({ ...org, id: "p-1", full_name: "Dana Buyer" });
        }
        return jsonResponse(org);
      },
      {
        org360: {
          ...org360,
          strength: {
            score: 41,
            bucket: "weak",
            contact_count: 3,
            contributor_person_id: "p-1",
            factors: {
              recency: 0.3,
              frequency: 0.2,
              reciprocity: 0.4,
              direction: 0.5,
            },
            last_interaction: "2026-06-20T12:00:00Z",
          },
          last_inbound_at: "2026-06-20T12:00:00Z",
          last_outbound_at: "2026-06-28T09:00:00Z",
        },
      },
    );
    render(<CompanyScreen id="o-1" />);

    // The way in, and that the relationship is live. WHICH side wrote last is
    // the daily brief's to say — acting on whose move it is belongs with the
    // moves, and the header states only that an exchange happened and when.
    await waitFor(() => expect(screen.getByText(/Way in/)).toBeTruthy());
    expect(screen.getByText(/of 3 contacts here/)).toBeTruthy();
    expect(screen.getByText(/Last exchange/)).toBeTruthy();
    // The composite is gone: it was PO-F-3's MAX over contacts, so one
    // talkative contact spoke for the account and "41/100" read as a verdict.
    expect(screen.queryByText(/41\/100/)).toBeNull();
  });

  it("says there is no relationship rather than showing a zero", async () => {
    stubFetch(async (url) => {
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    // org360's backstop omits `strength` entirely, which is what an account
    // with no readable contacts looks like: never contacted, no score.
    expect(screen.getByText("Never contacted")).toBeTruthy();
    expect(screen.queryByText(/^0 ·/)).toBeNull();
  });
});

describe("CompanyScreen — archived is read-only (P-3)", () => {
  it("hides edit/merge/archive and shows the Archived badge on an archived company", async () => {
    stubFetch(async (url) => {
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse({ ...org, archived_at: "2026-07-13T00:00:00Z" });
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() => expect(screen.getByText("Archived")).toBeTruthy());
    expect(screen.queryByTestId("edit-record")).toBeNull();
    expect(screen.queryByTestId("merge-record")).toBeNull();
    expect(screen.queryByTestId("archive-record")).toBeNull();
  });
});

describe("CompanyScreen — relationship kinds by scope (P-5)", () => {
  it("offers org↔org kinds (not deal_stakeholder) from a company and POSTs counterparty_org_id", async () => {
    const user = userEvent.setup();
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/relationships")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...employmentRel, id: "rel-new" }, 201);
      }
      if (
        url.includes("/relationships") &&
        url.includes("organization_id=o-1")
      ) {
        return emptyPage();
      }
      if (url.includes("/organizations?") && url.includes("q=acme")) {
        return jsonResponse({
          data: [{ id: "o-2", display_name: "Acme Corp" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await user.click(await screen.findByRole("button", { name: "Profile" }));
    await waitFor(() =>
      expect(screen.getByTestId("add-relationship")).toBeTruthy(),
    );
    await user.click(screen.getByTestId("add-relationship"));

    // An org anchors employment + the org↔org kinds; deal_stakeholder needs a
    // person endpoint and must not be offered here. The kinds only exist in the
    // DOM while the popup is open, so the absence is asserted on an open list.
    const kind = screen.getByLabelText("Kind");
    await user.click(kind);
    const kinds = screen.getByRole("listbox");
    expect(within(kinds).queryByText("Deal stakeholder")).toBeNull();
    await user.click(within(kinds).getByRole("option", { name: "Partner of" }));

    await user.type(screen.getByPlaceholderText("Search…"), "acme");
    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() => expect(screen.getByText("Acme Corp")).toBeTruthy());
    await user.click(screen.getByText("Acme Corp"));
    await user.click(screen.getByTestId("add-relationship-submit"));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      organization_id: "o-1",
      counterparty_org_id: "o-2",
      kind: "partner_of",
      source: "manual",
    });
    expect(posted).not.toHaveProperty("person_id");
  });
});

describe("CompanyScreen — the header's overflow menu", () => {
  // The panel keeps its items mounted so a dialog opened from one survives the
  // click that closes the menu — which means "closed" has to be asserted on
  // the panel, not on the absence of the items. A `display` rule in the
  // author stylesheet once beat the UA's `[hidden] {display:none}` and left
  // every destructive verb standing open in the header.
  it("keeps its items out of the page until the trigger is used", async () => {
    stubFetch(companyBackstop);
    render(<CompanyScreen id="o-1" />);

    const trigger = await screen.findByRole("button", { name: "More actions" });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    const panelId = trigger.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    const panel = document.getElementById(panelId ?? "");
    expect(panel?.hasAttribute("hidden")).toBe(true);

    await userEvent.click(trigger);

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(panel?.hasAttribute("hidden")).toBe(false);
  });
});

describe("CompanyScreen — the record's history", () => {
  // The audit spine is an inspection of the record, not part of the account's
  // story, so it opens from the header's overflow menu rather than standing
  // as a tab beside the timeline.
  it("opens the full history from the overflow menu", async () => {
    stubFetch(async (url) => {
      if (url.includes("/records/organization/o-1/history")) {
        return jsonResponse({
          data: [
            {
              id: "h1",
              actor_type: "human",
              actor_id: "u1",
              action: "create",
              occurred_at: "2026-07-13T10:00:00Z",
              summary: "Created the record",
            },
          ],
          page: { next_cursor: null },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(await openRecordMenu("company-full-history"));

    await waitFor(() =>
      expect(screen.getByText("Created the record")).toBeTruthy(),
    );
  });

  // Field changes are the account's own chronology, so they sit in the
  // timeline behind a filter — not on a screen of their own.
  it("shows field changes in the timeline under the Changes filter", async () => {
    stubFetch(async (url) => {
      if (url.includes("/field-history")) {
        return jsonResponse({
          data: [
            {
              id: "f1",
              entity_type: "organization",
              entity_id: "o-1",
              field: "industry",
              old_value: "Automotive",
              new_value: "Manufacturing",
              changed_at: "2026-07-14T10:00:00Z",
              actor_type: "human",
              actor_id: "u1",
            },
          ],
          page: { next_cursor: null },
        });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);
    await openHistory();

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Changes" })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Changes" }));

    // Scoped to the timeline: the account is called "Brandt Automotive GmbH",
    // so a page-wide match on the old value would pass on the heading.
    const timeline = await screen.findByRole("region", { name: "Timeline" });
    await waitFor(() =>
      expect(within(timeline).getByText("Manufacturing")).toBeTruthy(),
    );
    expect(within(timeline).getByText("Automotive")).toBeTruthy();
    expect(within(timeline).getByText("Industry")).toBeTruthy();
  });
});

// companyBackstop answers the record read the page shell needs and an empty
// page for everything else, so a suite exercising one card does not have to
// plumb every other request the screen fires.
async function companyBackstop(url: string): Promise<Response> {
  return url.endsWith("/organizations/o-1") ? jsonResponse(org) : emptyPage();
}

// One stalled-deal suggestion, as the 360 serves it. The reason is the part
// the rep judges, so the reason is what the tests assert on.
const stalledSuggestion = {
  kind: "stalled_deal",
  reason:
    '"Fleet retrofit 2026" has had no activity long enough to count as stalled.',
  fingerprint: "fp-stalled-1",
  subject_type: "deal",
  subject_id: "d-1",
  evidence: [{ entity_type: "deal", entity_id: "d-1" }],
};

// The suggestion rows and the ask card are components of their own, mounted
// here directly rather than through the company page, which renders neither.
// This matters for the "the card is absent" cases below: asserted against a
// page that never mounts one, they would hold no matter what the card did.
function renderSuggestionsFor(three60: unknown) {
  render(
    <SuggestionsSection
      orgId="o-1"
      view={three60 as never}
      onOpenRecord={() => {}}
      onPerform={() => {}}
    />,
  );
}

describe("CompanyScreen — next-step suggestions", () => {
  it("leads each suggestion with the reason the rule fired, and cites the record", async () => {
    const three60 = { ...org360, suggestions: [stalledSuggestion] };
    stubFetch(companyBackstop, { org360: three60 });
    renderSuggestionsFor(three60);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.getByText("Stalled deal")).toBeTruthy();
    // The evidence is reachable: a suggestion the rep cannot check is a verdict.
    expect(screen.getByRole("button", { name: "deal" })).toBeTruthy();
  });

  it("names how many suggestions the card left out", async () => {
    const three60 = {
      ...org360,
      suggestions: [stalledSuggestion],
      suggestions_dropped: 3,
    };
    stubFetch(companyBackstop, { org360: three60 });
    renderSuggestionsFor(three60);

    // A truncated list with no count reads as "that is everything".
    await waitFor(() =>
      expect(screen.getByText("3 more not shown here.")).toBeTruthy(),
    );
  });

  it("stays silent about what it left out when there is nothing left out", async () => {
    // Zero is the ordinary case, so the "N more" line must not render on it —
    // otherwise every card carries "0 more not shown here."
    const three60 = {
      ...org360,
      suggestions: [stalledSuggestion],
      suggestions_dropped: 0,
    };
    stubFetch(companyBackstop, { org360: three60 });
    renderSuggestionsFor(three60);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.queryByText(/more not shown here/)).toBeNull();
  });

  it("stays silent about what it left out when the count is absent", async () => {
    // Absent means the section was never computed. A "0 more" line would state a
    // fact about an account this read did not look at.
    const three60 = {
      ...org360,
      suggestions: [stalledSuggestion],
      suggestions_dropped: undefined,
    };
    stubFetch(companyBackstop, { org360: three60 });
    renderSuggestionsFor(three60);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.queryByText(/more not shown here/)).toBeNull();
  });

  it("says nothing at all when the account needs nothing", async () => {
    stubFetch(companyBackstop);
    renderSuggestionsFor(org360);

    // "No advice" is not something a rep acts on, so the card is absent
    // rather than empty. Asserted against a MOUNTED brief: on a page that
    // never renders one, this would hold no matter what the component did.
    await waitFor(() =>
      expect(screen.queryByText("Worth doing next")).toBeNull(),
    );
  });

  it("stays silent rather than claiming no advice when the section is withheld", async () => {
    const three60 = {
      ...org360,
      suggestions: undefined,
      sections_omitted: ["suggestions"],
    };
    stubFetch(companyBackstop, { org360: three60 });
    renderSuggestionsFor(three60);

    await waitFor(() =>
      expect(screen.queryByText("Worth doing next")).toBeNull(),
    );
  });

  it("dismisses by fingerprint and leaves the row for the server to remove", async () => {
    let dismissed: unknown;
    stubFetch(
      async (url, method, request) => {
        if (method === "POST" && url.includes("/suggestions/dismiss")) {
          dismissed = await request.json();
          return new Response(null, { status: 204 });
        }
        return companyBackstop(url);
      },
      { org360: { ...org360, suggestions: [stalledSuggestion] } },
    );
    renderSuggestionsFor({ ...org360, suggestions: [stalledSuggestion] });

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));

    await waitFor(() => expect(dismissed).toBeTruthy());
    // The server decides what survives: the card sends the fingerprint and
    // does NOT hide the row itself. Whether the surrounding page then re-reads
    // the 360 is the page's business, and this suite mounts the card alone.
    expect(dismissed).toEqual({ fingerprint: "fp-stalled-1" });
    expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy();
  });

  it("says a dismissal failed instead of leaving the click looking like a miss", async () => {
    stubFetch(
      async (url, method) => {
        if (method === "POST" && url.includes("/suggestions/dismiss")) {
          return jsonResponse({ title: "nope" }, 500);
        }
        return companyBackstop(url);
      },
      { org360: { ...org360, suggestions: [stalledSuggestion] } },
    );
    renderSuggestionsFor({ ...org360, suggestions: [stalledSuggestion] });

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));

    await waitFor(() =>
      expect(screen.getByText(/could not be dismissed/)).toBeTruthy(),
    );
    // The row is still there, which is what the notice is telling the reader.
    expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy();
  });
});

describe("CompanyScreen — Ask Margince", () => {
  const answer = {
    organization_id: "o-1",
    question: "whats_open",
    generated_at: "2026-06-01T09:00:00Z",
    generated_by: "model",
    sentences: [
      {
        text: "Two open deals, worth about 57000 EUR.",
        evidence: [{ entity_type: "deal", entity_id: "d-1" }],
      },
    ],
  };

  it("asks only the prepared questions, and shows which one the answer answers", async () => {
    let asked: unknown;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.endsWith("/ask")) {
        asked = await request.json();
        return jsonResponse(answer);
      }
      return companyBackstop(url);
    });
    render(<AssistantPanel orgId="o-1" enabled onOpenRecord={() => {}} />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "What's open here?" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() => expect(asked).toEqual({ question: "whats_open" }));
    await waitFor(() =>
      expect(
        screen.getByText("Two open deals, worth about 57000 EUR."),
      ).toBeTruthy(),
    );
    // Which writer produced it is never implied.
    expect(screen.getByText("Written by Margince")).toBeTruthy();
    // The question is repeated over its answer, so a reader who has scrolled
    // cannot pair the wrong one with it.
    expect(screen.getAllByText("What's open here?").length).toBeGreaterThan(1);
  });

  it("says there is nothing to answer from rather than nothing at all", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.endsWith("/ask")) {
        return jsonResponse({ ...answer, sentences: [] });
      }
      return companyBackstop(url);
    });
    render(<AssistantPanel orgId="o-1" enabled onOpenRecord={() => {}} />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "What's open here?" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() =>
      expect(screen.getByText(/Nothing here that you can see/)).toBeTruthy(),
    );
  });

  it("reports a failed question instead of leaving the card blank", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.endsWith("/ask")) {
        return jsonResponse({ title: "nope" }, 500);
      }
      return companyBackstop(url);
    });
    render(<AssistantPanel orgId="o-1" enabled onOpenRecord={() => {}} />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "What's open here?" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "What's open here?" }),
    );

    await waitFor(() =>
      expect(screen.getByText(/could not be answered/)).toBeTruthy(),
    );
  });
});

// ONE column beside the left rail (mockup State D). The page had a work
// column and a context column beside it; the context column moved to the
// LEFT rail so the story keeps the wider share, and the composer opens as its
// own overlay drawer rather than into a column. Tags and lists moved into the
// rail's own panel too — the business grid that used to hold them is gone,
// which is the obligation these cases keep: a layout change must not become
// an availability change.
describe("CompanyScreen — State D's one column and its card grid", () => {
  it("puts the account's context on the left, beside the work", async () => {
    stubFetch(companyBackstop, { org360 });
    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");

    await waitFor(() =>
      expect(container.querySelector(".co-panel-stack")).toBeTruthy(),
    );
    // Two columns, not three: the context beside the work. A right-hand
    // aside would be a third place to look, and the mockups draw none.
    expect(container.querySelector(".record-rail")).toBeTruthy();
    expect(container.querySelector(".co-rail")).toBeTruthy();
    expect(container.querySelector(".record-aside")).toBeNull();
  });

  // Every card is still ON the page, wherever it sits. Named individually
  // rather than counted: a count passes on a layout that lost one card and
  // grew another, and moving a card between columns must never be the way one
  // disappears. The pipeline moved off the grid onto its own Deals tab, and
  // tags/lists moved into the rail, so both are asserted there rather than in
  // an Overview grid that no longer exists.
  it("carries every panel of the overview stack, and files what is left in the rail", async () => {
    stubFetch(companyBackstop, { org360 });
    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");

    // The overview stack: the account, what it is worth, the pipeline's own
    // figures, and the money. "Worth doing next" is not asserted here — it is
    // advice, and this fixture's account has none to give; the suggestions
    // suite above exercises its own presence.
    const stack = container.querySelector(".co-panel-stack");
    expect(stack).toBeTruthy();
    for (const panel of ["The account, in short", "Commercial", "Finance"]) {
      expect(stack?.textContent).toContain(panel);
    }
    expect(stack?.textContent).not.toContain("Lists & tags");

    // The pipeline and the commercial picture have their own tab too.
    await userEvent.click(screen.getByRole("button", { name: "Deals" }));
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Deals" })).toBeTruthy(),
    );

    // The relationship around it, and how the account is filed, both live in
    // the rail rather than off the page.
    const rail = container.querySelector(".co-rail");
    expect(rail).toBeTruthy();
    for (const card of ["People", "Signals", "Lists & tags"]) {
      expect(rail?.textContent).toContain(card);
    }
  });

  // The drawer opens INTO the rail's column. Both composers do — the header's
  // Write-email and the one anchored on a message — so the rail stands down
  // for either, and comes back when the drawer closes.
  it("stands the rail down while a composer holds its column", async () => {
    stubFetch(companyBackstop, { org360 });
    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");
    await waitFor(() =>
      expect(container.querySelector(".co-rail")).toBeTruthy(),
    );

    await userEvent.click(screen.getByRole("button", { name: "Write email" }));
    await waitFor(() => expect(container.querySelector(".co-rail")).toBeNull());

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(container.querySelector(".co-rail")).toBeTruthy(),
    );
  });

  // A call or a note often carries no subject. Counting only the subjected
  // rows would draw "nothing logged with them yet" on an account that has been
  // called five times — a false statement about the account, made from a fact
  // about a row.
  it("counts every logged activity, not only the ones with a subject", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        activities: {
          data: [
            {
              id: "a-1",
              kind: "call",
              occurred_at: "2026-06-01T08:30:00Z",
              direction: "inbound",
            },
          ],
          page: { has_more: false, next_cursor: null },
        },
      },
    });
    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");

    const stack = container.querySelector(".co-panel-stack");
    await waitFor(() =>
      expect(stack?.textContent).toContain("Recent activity"),
    );
    expect(stack?.textContent).not.toContain("Nothing logged with them yet");
  });

  // None of the reference cards comes from the 360 — each runs its own read —
  // so they must be on the page before that read lands, and stay there if it
  // never does.
  it("offers the reference cards before the 360 read lands", async () => {
    let releaseView: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      releaseView = resolve;
    });
    const { fetchMock } = stubFetch(companyBackstop);
    const held360 = vi.fn(async (request: Request) => {
      if (new URL(request.url).pathname.endsWith("/360")) {
        await held;
      }
      return fetchMock(request);
    });
    vi.stubGlobal("fetch", held360);

    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");
    await openProfile();

    // Asserted on the disclosure summaries a reader actually clicks: these
    // words also appear in the app's navigation, so a bare text match would
    // pass on a page that had lost every one of these cards.
    const summaries = () =>
      Array.from(container.querySelectorAll(".disclosure-summary")).map((el) =>
        el.textContent?.trim(),
      );
    await waitFor(() => expect(summaries()).toContain("Company profile"));
    expect(summaries()).toContain("Where this came from");
    expect(summaries()).toContain("Data & tools");
    releaseView?.();
  });
});

// The 360 caps its activities section. A capped list that says nothing reads
// as the whole history: the rep takes the oldest row on screen for the day the
// relationship started.
describe("CompanyScreen — the timeline says where it stops", () => {
  it("says the activity list is cut when the 360 reports more", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        activities: {
          data: [
            {
              id: "a-1",
              kind: "email",
              subject: "Re: Lead Gen",
              occurred_at: "2026-06-01T08:30:00Z",
              direction: "inbound",
            },
          ],
          page: { has_more: true, next_cursor: "c1" },
        },
      },
    });
    render(<CompanyScreen id="o-1" />);
    await openHistory();

    await waitFor(() =>
      expect(screen.getAllByText("Re: Lead Gen").length).toBeGreaterThan(0),
    );
    expect(
      screen.getByText(
        "There are more activities here than fit. Only the most recent ones are listed.",
      ),
    ).toBeTruthy();
  });

  it("stays silent when the account's whole activity list is on screen", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        activities: {
          data: [
            {
              id: "a-1",
              kind: "email",
              subject: "Re: Lead Gen",
              occurred_at: "2026-06-01T08:30:00Z",
              direction: "inbound",
            },
          ],
          page: { has_more: false, next_cursor: null },
        },
      },
    });
    render(<CompanyScreen id="o-1" />);
    await openHistory();

    await waitFor(() =>
      expect(screen.getAllByText("Re: Lead Gen").length).toBeGreaterThan(0),
    );
    expect(screen.queryByText(/more activities than fit here/)).toBeNull();
  });
});

// `UpdateOrganizationRequest.owner_id` cannot carry "unassign" — a null is
// indistinguishable from an omitted field on the wire. An optional select
// offers a blank option, so picking it took the answer and dropped it: the
// save reported success and the old owner stayed responsible.
describe("companyEditFields — the owner select never offers what it cannot save", () => {
  const ownerField = (hasOwner: boolean) =>
    companyEditFields(
      [{ id: "u1", display_name: "Demo Admin" }],
      hasOwner,
      // The identity translator: this suite is about the owner select's
      // required-ness, and a key reads as well as a word for that.
      (key) => key,
    ).find((field) => field.key === "owner_id");

  it("is required while the account has an owner, so no blank option renders", () => {
    // An optional select renders a blank option; picking it would send a body
    // the server reads as "leave the owner alone".
    expect(ownerField(true)?.required).toBe(true);
  });

  it("stays optional while the account has no owner at all", () => {
    // There the blank is the truthful current state, not an edit we cannot make.
    expect(ownerField(false)?.required).toBeFalsy();
  });
});

// The filter belongs to the account being read, not to the session: the route
// swaps one company for another without unmounting, so a reader who checked
// Changes once met Changes on every account afterwards.
describe("CompanyScreen — the timeline filter does not follow you", () => {
  it("returns to Activities when another company is opened", async () => {
    stubFetch(async (url) =>
      /\/organizations\/o-\d$/.test(new URL(url).pathname)
        ? jsonResponse(org)
        : emptyPage(),
    );
    // One QueryClient across both renders, with BOTH records already cached:
    // that is what keeps the record component mounted across the swap, and a
    // cold second record would unmount it and reset the filter for free.
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["organization", "o-1"], org);
    client.setQueryData(["organization", "o-2"], { ...org, id: "o-2" });
    const page = (id: string) => (
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <CompanyScreen id={id} />
        </LocaleProvider>
      </QueryClientProvider>
    );
    const { rerender } = rtlRender(page("o-1"));
    await openHistory();

    const pressed = (name: string) =>
      screen.getByRole("button", { name }).getAttribute("aria-pressed");

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Changes" })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Changes" }));
    await waitFor(() => expect(pressed("Changes")).toBe("true"));

    rerender(page("o-2"));
    await openHistory();

    await waitFor(() => expect(pressed("Activities")).toBe("true"));
  });
});

describe("CompanyScreen — the active tab is scoped to the account being read", () => {
  it("opens on Overview when another company is opened", async () => {
    stubFetch(async (url) =>
      /\/organizations\/o-\d$/.test(new URL(url).pathname)
        ? jsonResponse(org)
        : emptyPage(),
    );
    // One QueryClient across both renders, with BOTH records already cached:
    // that is what keeps the record component mounted across the swap, the
    // same setup the chronology filter's own test above uses.
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["organization", "o-1"], org);
    client.setQueryData(["organization", "o-2"], { ...org, id: "o-2" });
    const page = (id: string) => (
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <CompanyScreen id={id} />
        </LocaleProvider>
      </QueryClientProvider>
    );
    const { rerender } = rtlRender(page("o-1"));
    const pressed = (name: string) =>
      screen.getByRole("button", { name }).getAttribute("aria-pressed");

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Documents" })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Documents" }));
    await waitFor(() => expect(pressed("Documents")).toBe("true"));

    rerender(page("o-2"));

    await waitFor(() => expect(pressed("Overview")).toBe("true"));
  });
});

describe("CompanyScreen — the Partner tab is scoped to the account being read", () => {
  // An org with a programme carries "partner" in relationship_types
  // (ADR-0079/A124); one without it must never offer the tab, and must never
  // inherit it from whichever account was open before.
  const partnerOrg = { ...org, relationship_types: ["partner"] as const };
  const nonPartnerOrg = { ...org, id: "o-2" };

  function stubTwoOrgs() {
    stubFetch(async (url) => {
      const pathname = new URL(url).pathname;
      if (pathname.endsWith("/organizations/o-1")) {
        return jsonResponse(partnerOrg);
      }
      if (pathname.endsWith("/organizations/o-2")) {
        return jsonResponse(nonPartnerOrg);
      }
      return emptyPage();
    });
  }

  // One QueryClient across both renders, with BOTH records already cached —
  // that is what keeps the record component mounted across the swap rather
  // than remounting it, the one condition under which a leak would show
  // (the same setup the chronology filter's own cross-record test uses).
  function renderTwoOrgs() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["organization", "o-1"], partnerOrg);
    client.setQueryData(["organization", "o-2"], nonPartnerOrg);
    const page = (id: string) => (
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <CompanyScreen id={id} />
        </LocaleProvider>
      </QueryClientProvider>
    );
    return { ...rtlRender(page("o-1")), page };
  }

  it("does not carry the Partner tab to an account with no programme", async () => {
    stubTwoOrgs();
    const { rerender, page } = renderTwoOrgs();

    await userEvent.click(
      await screen.findByRole("button", { name: "Partner" }),
    );
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "Partner" })
          .getAttribute("aria-pressed"),
      ).toBe("true"),
    );

    rerender(page("o-2"));

    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "Overview" })
          .getAttribute("aria-pressed"),
      ).toBe("true"),
    );
    expect(screen.queryByRole("button", { name: "Partner" })).toBeNull();
  });

  // The carveout that keeps the tab reachable — companyTabsFor's
  // `tab === "partner"` branch — has to survive the fix above: a check that
  // only proved the leak was gone could equally be satisfied by removing the
  // reader's only way to a first partner row.
  it("still opens the set-up-partner form on an account with no programme", async () => {
    stubFetch(companyBackstop);
    render(<CompanyScreen id="o-1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: "More actions" }),
    );
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Set up partner programme",
      }),
    );

    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "Partner" })
          .getAttribute("aria-pressed"),
      ).toBe("true"),
    );
  });
});

// One open, dated task, as the 360 serves it on the Tasks tab.
const openTask = {
  activity_id: "a-1",
  subject: "Follow up on contract renewal",
  due_at: "2026-08-20T00:00:00Z",
  overdue: false,
  assignee_id: null,
  linked_deal_id: null,
  linked_person_id: null,
};

async function openTasksTab() {
  await userEvent.click(await screen.findByRole("button", { name: "Tasks" }));
}

describe("CompanyScreen — the Tasks tab", () => {
  it("completes a task without leaving the account", async () => {
    let patched: unknown;
    stubFetch(
      async (url, method, request) => {
        if (method === "PATCH" && url.endsWith("/activities/a-1")) {
          patched = await request.json();
          return jsonResponse({});
        }
        return companyBackstop(url);
      },
      {
        org360: {
          ...org360,
          next_steps: { ...org360.next_steps, data: [openTask] },
        },
      },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab();

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("checkbox", { name: "Done" }));

    await waitFor(() => expect(patched).toEqual({ is_done: true }));
  });

  it("says the section is withheld rather than rendering it as empty", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        next_steps: undefined,
        sections_omitted: ["next_steps"],
      },
    });
    render(<CompanyScreen id="o-1" />);
    await openTasksTab();

    await waitFor(() =>
      expect(
        screen.getByText("Hidden — your role cannot read this"),
      ).toBeTruthy(),
    );
    expect(screen.queryByText("No open task on this account.")).toBeNull();
  });

  it("shows no task-completing verb on an archived account", async () => {
    // The server refuses a write on an archived account, so the tab omits the
    // verb rather than offering a button that can only 404.
    stubFetch(
      async (url) => {
        if (url.endsWith("/organizations/o-1")) {
          return jsonResponse({ ...org, archived_at: "2026-07-13T00:00:00Z" });
        }
        if (url.endsWith("/activities/a-1")) {
          return jsonResponse({
            id: openTask.activity_id,
            organization_id: "o-1",
            type: "task",
            subject: openTask.subject,
            occurred_at: "2026-08-01T09:00:00Z",
            due_at: openTask.due_at,
            is_done: false,
            captured_by: "human:u1",
            source: "manual",
            version: 1,
          });
        }
        return emptyPage();
      },
      {
        org360: {
          ...org360,
          next_steps: { ...org360.next_steps, data: [openTask] },
        },
      },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab();

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    expect(screen.queryByRole("checkbox", { name: "Done" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Snooze 1d" })).toBeNull();

    // The same withheld verb holds inside the detail modal, not only the row.
    await userEvent.click(screen.getByText(openTask.subject));
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: openTask.subject }),
      ).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: "Done" })).toBeNull();
  });

  // The detail modal renders on this tab and nowhere else, so a tab change
  // takes it off screen without its own onClose ever running. An open id that
  // survived that would be waiting the next time the reader came back to
  // Tasks — a dialog reopening itself, having been closed by nobody.
  //
  // Driven through the tab pill on purpose. A reader cannot reach the pill
  // while the dialog covers it, and that is a fact about Modal's backdrop
  // rather than about this page: the id must not depend on it.
  it("opens no dialog when the reader returns to Tasks after changing tab", async () => {
    const user = userEvent.setup();
    stubFetch(
      async (url) => {
        if (url.endsWith("/activities/a-1")) {
          return jsonResponse({
            id: openTask.activity_id,
            organization_id: "o-1",
            type: "task",
            subject: openTask.subject,
            occurred_at: "2026-08-01T09:00:00Z",
            due_at: openTask.due_at,
            is_done: false,
            captured_by: "human:u1",
            source: "manual",
            version: 1,
          });
        }
        return companyBackstop(url);
      },
      {
        org360: {
          ...org360,
          next_steps: { ...org360.next_steps, data: [openTask] },
        },
      },
    );
    render(<CompanyScreen id="o-1" />);
    await openTasksTab();
    await user.click(await screen.findByText(openTask.subject));
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: openTask.subject }),
      ).toBeTruthy(),
    );

    await user.click(screen.getByRole("button", { name: "Deals" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: openTask.subject }),
      ).toBeNull(),
    );
    await user.click(screen.getByRole("button", { name: "Tasks" }));

    await waitFor(() =>
      expect(screen.getByText(openTask.subject)).toBeTruthy(),
    );
    expect(screen.queryByRole("dialog", { name: openTask.subject })).toBeNull();
  });
});
