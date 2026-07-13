/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompaniesScreen, CompanyScreen } from "./organizations";

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

// A URL-capturing fetch stub shared across the P-14/15/16 wiring tests
// below: every request is recorded so a test can assert the params it
// carried, and a caller-supplied responder decides what comes back.
function stubFetch(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response>,
): { fetchMock: ReturnType<typeof vi.fn>; urls: string[] } {
  const urls: string[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    urls.push(request.url);
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
    expect(patchBody).not.toHaveProperty("domains");
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
