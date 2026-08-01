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
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompaniesScreen, CompanyScreen, mapOrgUpdate } from "./organizations";

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
  workspace_id: "w",
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

describe("company-360 enrichment", () => {
  it("stages an evidence-backed proposal: human labels, confidence, confirm-first banner", async () => {
    stubApi(() => jsonResponse(proposal));
    render(<CompanyScreen id="o-1" />);
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
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
): { fetchMock: ReturnType<typeof vi.fn>; urls: string[] } {
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
      fireEvent.change(screen.getByRole("searchbox"), {
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

  it("shows Load more on has_more and fetches the next cursor on click", async () => {
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

    const loadMore = screen.getByRole("button", { name: "Load more" });
    await userEvent.click(loadMore);

    await waitFor(() =>
      expect(screen.getByText("Nordwind Logistik")).toBeTruthy(),
    );
    expect(urls.some((url) => url.includes("cursor=c1"))).toBe(true);
  });
});

describe("CompaniesScreen — rich create (P-15)", () => {
  it("posts display_name + size_band + domains + source:manual on submit", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/organizations")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...org, id: "o-new" }, 201);
      }
      return emptyPage();
    });
    render(<CompaniesScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(
      screen.getByLabelText("Company name *"),
      "Otto Fischer GmbH",
    );
    await userEvent.selectOptions(
      screen.getByLabelText("Company size"),
      "11-50",
    );
    await userEvent.click(screen.getByText("Add domain"));
    await userEvent.type(screen.getByLabelText("Domain *"), "otto.example");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

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
  workspace_id: "w",
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

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));

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
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));
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

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());

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

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());

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

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());

    await waitFor(() =>
      expect(
        screen.getByText("1 account(s) not visible to you were excluded"),
      ).toBeTruthy(),
    );
  });
});

describe("CompanyScreen — the account pulse line (P-4)", () => {
  it("leads with the score, who carries it, and how many contacts it beat", async () => {
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
        },
      },
    );
    render(<CompanyScreen id="o-1" />);

    // The contact, the count and the score all read off the one composite
    // response — no second round trip for the header. The line leads with the
    // person because that is what a rep acts on; the score follows, labelled,
    // because a bare number scales to nothing.
    await waitFor(() =>
      expect(screen.getByText(/Strongest contact/)).toBeTruthy(),
    );
    expect(screen.getByText(/of 3 people here/)).toBeTruthy();
    expect(screen.getByText(/relationship 41\/100/)).toBeTruthy();
    expect(screen.getByText(/Last touch/)).toBeTruthy();
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
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));
    await waitFor(() =>
      expect(screen.getByTestId("add-relationship")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("add-relationship"));

    // An org anchors employment + the org↔org kinds; deal_stakeholder needs a
    // person endpoint and must not be offered here.
    const kind = screen.getByLabelText("Kind");
    expect(within(kind).queryByText("Deal stakeholder")).toBeNull();
    await userEvent.selectOptions(kind, "partner_of");

    await userEvent.type(screen.getByPlaceholderText("Search…"), "acme");
    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() => expect(screen.getByText("Acme Corp")).toBeTruthy());
    await userEvent.click(screen.getByText("Acme Corp"));
    await userEvent.click(screen.getByTestId("add-relationship-submit"));

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

describe("CompanyScreen — next-step suggestions", () => {
  it("leads each suggestion with the reason the rule fired, and cites the record", async () => {
    stubFetch(companyBackstop, {
      org360: { ...org360, suggestions: [stalledSuggestion] },
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.getByText("Stalled deal")).toBeTruthy();
    // The evidence is reachable: a suggestion the rep cannot check is a verdict.
    expect(screen.getByRole("button", { name: "deal" })).toBeTruthy();
  });

  it("names how many suggestions the card left out", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        suggestions: [stalledSuggestion],
        suggestions_dropped: 3,
      },
    });
    render(<CompanyScreen id="o-1" />);

    // A truncated list with no count reads as "that is everything".
    await waitFor(() =>
      expect(screen.getByText("3 more not shown here.")).toBeTruthy(),
    );
  });

  it("stays silent about what it left out when there is nothing left out", async () => {
    // Zero is the ordinary case, so the "N more" line must not render on it —
    // otherwise every card carries "0 more not shown here."
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        suggestions: [stalledSuggestion],
        suggestions_dropped: 0,
      },
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.queryByText(/more not shown here/)).toBeNull();
  });

  it("stays silent about what it left out when the count is absent", async () => {
    // Absent means the section was never computed. A "0 more" line would state a
    // fact about an account this read did not look at.
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        suggestions: [stalledSuggestion],
        suggestions_dropped: undefined,
      },
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    expect(screen.queryByText(/more not shown here/)).toBeNull();
  });

  it("says nothing at all when the account needs nothing", async () => {
    stubFetch(companyBackstop);
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    // "No advice" is not something a rep acts on, so the card is absent
    // rather than empty.
    expect(screen.queryByText("Worth doing next")).toBeNull();
  });

  it("stays silent rather than claiming no advice when the section is withheld", async () => {
    stubFetch(companyBackstop, {
      org360: {
        ...org360,
        suggestions: undefined,
        sections_omitted: ["suggestions"],
      },
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    expect(screen.queryByText("Worth doing next")).toBeNull();
  });

  it("dismisses by fingerprint, and re-reads the 360 rather than hiding the row itself", async () => {
    let dismissed: unknown;
    const { urls } = stubFetch(
      async (url, method, request) => {
        if (method === "POST" && url.includes("/suggestions/dismiss")) {
          dismissed = await request.json();
          return new Response(null, { status: 204 });
        }
        return companyBackstop(url);
      },
      { org360: { ...org360, suggestions: [stalledSuggestion] } },
    );
    render(<CompanyScreen id="o-1" />);

    await waitFor(() =>
      expect(screen.getByText(stalledSuggestion.reason)).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));

    await waitFor(() => expect(dismissed).toBeTruthy());
    expect(dismissed).toEqual({ fingerprint: "fp-stalled-1" });
    // The server decides what survives: the row goes when the re-read says so.
    await waitFor(() =>
      expect(urls.filter((u) => u.endsWith("/360")).length).toBeGreaterThan(1),
    );
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
    render(<CompanyScreen id="o-1" />);

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
    render(<CompanyScreen id="o-1" />);

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
    render(<CompanyScreen id="o-1" />);

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
    render(<CompanyScreen id="o-1" />);

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

// The page must not re-column itself under the reader. RecordView picks its
// grid template from which zones are present, so a right rail that arrives
// with the composite read moves the whole middle column — and everything the
// reader was looking at — sideways the moment it lands.
describe("CompanyScreen — the layout does not shift as the read lands", () => {
  it("holds the three-column template while the 360 is still in flight", async () => {
    let releaseView: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      releaseView = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const pathname = new URL(request.url).pathname;
        if (pathname.endsWith("/360")) {
          await held;
          return jsonResponse(org360);
        }
        if (pathname.endsWith("/hierarchy-rollup")) {
          return jsonResponse(emptyRollup);
        }
        if (pathname.endsWith("/organizations/o-1")) {
          return jsonResponse(org);
        }
        return jsonResponse({
          data: [],
          page: { has_more: false, next_cursor: null },
        });
      }),
    );
    const { container } = render(<CompanyScreen id="o-1" />);
    await screen.findByText("Brandt Automotive GmbH");
    const zonesWhileLoading = container.querySelector(".record-zones");
    expect(zonesWhileLoading?.className).toContain("record-zones-both");
    releaseView?.();
    await waitFor(() =>
      expect(container.querySelector(".record-zones")?.className).toContain(
        "record-zones-both",
      ),
    );
  });
});
