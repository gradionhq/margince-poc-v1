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
  options?: Readonly<{ strength?: unknown }>,
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
    return responder(request.url, request.method, request);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, urls };
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

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
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

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
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
      expect(screen.getByText("Value proposition")).toBeTruthy(),
    );
    expect(screen.getByText("Fleet retrofits without downtime")).toBeTruthy();
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
    expect(screen.getByText("Company")).toBeTruthy();
    expect(screen.getByText("Offering")).toBeTruthy();
    expect(screen.getByText("Market")).toBeTruthy();
    expect(screen.getByText("1998")).toBeTruthy();
    expect(screen.getByText("Automotive OEMs")).toBeTruthy();
    expect(screen.getByText("Fleet retrofits")).toBeTruthy();
    // No signal fact was returned, so that subsection is absent.
    expect(screen.queryByText("Signals")).toBeNull();
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

    await waitFor(() =>
      expect(screen.getByTestId("archive-record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("archive-record"));
    await userEvent.click(screen.getByTestId("archive-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
    expect(window.location.hash).toBe("#/companies");
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

    await waitFor(() =>
      expect(screen.getByTestId("merge-record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("merge-record"));
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

describe("CompanyScreen — Roll-up tab (P-7)", () => {
  it("shows the weighted pipeline, closed-won, activity, and account figures", async () => {
    stubFetch(async (url) => {
      if (url.includes("/hierarchy-rollup")) {
        return jsonResponse(rollup);
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Roll-up"));

    await waitFor(() => expect(screen.getByText("€48,000.00")).toBeTruthy());
    expect(screen.getByText("€12,000.00")).toBeTruthy();
    expect(screen.getByText("12")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
  });

  it("renders the honest FX-unavailable message instead of zeros on a 422", async () => {
    stubFetch(async (url) => {
      if (url.includes("/hierarchy-rollup")) {
        return jsonResponse(
          { title: "Unprocessable", code: "fx_rate_unavailable" },
          422,
        );
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Roll-up"));

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
    stubFetch(async (url) => {
      if (url.includes("/hierarchy-rollup")) {
        return jsonResponse({
          ...rollup,
          restricted_excluded: [
            { id: "o-9", display_name: "Hidden Subsidiary GmbH" },
          ],
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(org);
    });
    render(<CompanyScreen id="o-1" />);

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Roll-up"));

    await waitFor(() =>
      expect(
        screen.getByText("1 account(s) not visible to you were excluded"),
      ).toBeTruthy(),
    );
  });
});

describe("CompanyScreen — relationship-strength card (P-4)", () => {
  it("renders the org's strength bucket, score, and factor breakdown", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(org);
      },
      {
        strength: {
          score: 41,
          bucket: "weak",
          factors: {
            recency: 0.3,
            frequency: 0.2,
            reciprocity: 0.4,
            direction: 0.5,
          },
          last_interaction: "2026-06-20T12:00:00Z",
        },
      },
    );
    render(<CompanyScreen id="o-1" />);

    await waitFor(() => expect(screen.getByText("Weak")).toBeTruthy());
    expect(screen.getByText("Score 41/100")).toBeTruthy();
    expect(screen.getByText("Recency")).toBeTruthy();
    expect(screen.getByText("Frequency")).toBeTruthy();
    expect(screen.getByText("Reciprocity")).toBeTruthy();
    expect(screen.getByText("Direction")).toBeTruthy();
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

describe("CompanyScreen — History tab", () => {
  it("shows a History tab that lists record changes", async () => {
    stubFetch(async (url) => {
      if (url.includes("/history")) {
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

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /history/i })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: /history/i }));

    await waitFor(() =>
      expect(screen.getByText("Created the record")).toBeTruthy(),
    );
  });
});
