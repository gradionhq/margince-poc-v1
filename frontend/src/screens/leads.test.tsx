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
import { LeadScreen, LeadsScreen, promoteEligible, scoreTone } from "./leads";

// Leads (B-EP09.10a/b, §3.5 segregation): visually SEGREGATED from the
// contact graph — the ≥60/40–59/<40 score thresholds, eligibility-gated
// promote, and a lead row navigating to the LEAD detail (never the person
// screen). Below that: the same P-14/15/16/1 shared-block wiring as contacts
// (people.test.tsx) and companies (organizations.test.tsx) — search/sort/
// pagination + a status filter, the rich create modal (full_name/email/
// linkedin_url/company_name/candidate_org_key), the lead-360 If-Match edit
// (Promote + badges preserved), and the duplicate_email dedupe link.

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

const anna = {
  id: "p-1",
  workspace_id: "w-1",
  full_name: "Anna Weber",
  captured_by: "human:u-1",
  source: "manual",
  version: 1,
};

const lead = {
  id: "l-1",
  workspace_id: "w-1",
  full_name: "Jonas Petersen",
  email: "jonas@nordwind.example",
  company_name: "Nordwind Logistik",
  status: "working" as const,
  score: 72,
  captured_by: "human:u-1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-20T08:00:00Z",
};

describe("score thresholds (AC-leads colour bands)", () => {
  it("maps ≥60 accent-strong, 40–59 medium, <40 low", () => {
    expect(scoreTone(60)).toBe("success");
    expect(scoreTone(95)).toBe("success");
    expect(scoreTone(59)).toBe("warn");
    expect(scoreTone(40)).toBe("warn");
    expect(scoreTone(39)).toBe("danger");
  });
});

describe("promote eligibility gate", () => {
  it("requires an open status and an email", () => {
    expect(promoteEligible(lead)).toBe(true);
    expect(promoteEligible({ ...lead, status: "promoted" })).toBe(false);
    expect(promoteEligible({ ...lead, status: "disqualified" })).toBe(false);
    expect(promoteEligible({ ...lead, email: null })).toBe(false);
  });
});

describe("LeadsScreen + LeadScreen (B-EP09.10b, §3.5 segregation)", () => {
  it("a lead row navigates to the LEAD detail, not the person screen", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [lead], page: { next_cursor: null } }),
      ),
    );
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("Jonas Petersen"));
    expect(window.location.hash).toBe("#/leads/l-1");
  });

  it("promote posts and lands on the resulting person 360", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method =
          input instanceof Request ? input.method : (init?.method ?? "GET");
        if (method === "POST" && url.includes("/leads/l-1/promote")) {
          return jsonResponse({ person: anna, merged: false, lead_id: "l-1" });
        }
        return jsonResponse(lead);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<LeadScreen id="l-1" />);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Promote to contact" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Promote to contact" }),
    );
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/p-1"));
  });

  it("promote is disabled for an ineligible lead", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ ...lead, status: "promoted" })),
    );
    render(<LeadScreen id="l-1" />);
    await waitFor(() =>
      expect(
        (
          screen.getByRole("button", {
            name: "Promote to contact",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(true),
    );
    expect(screen.getByText("needs an email and an open status")).toBeTruthy();
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

describe("LeadsScreen — search/sort/pagination + status filter (P-14)", () => {
  it("carries the debounced search term into the next fetch", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<LeadsScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByRole("searchbox"), {
        target: { value: "jonas" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() =>
      expect(urls.some((url) => url.includes("q=jonas"))).toBe(true),
    );
  });

  it("sends status=working when the status filter is set", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<LeadsScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    await userEvent.selectOptions(screen.getByLabelText("Status"), "working");

    await waitFor(() =>
      expect(urls.some((url) => url.includes("status=working"))).toBe(true),
    );
  });

  it("shows Load more on has_more and fetches the next cursor on click", async () => {
    const { urls } = stubFetch(async (url) => {
      if (url.includes("cursor=c1")) {
        return jsonResponse({
          data: [{ ...lead, id: "l-2", full_name: "Otto Fischer" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        data: [lead],
        page: { next_cursor: "c1", has_more: true },
      });
    });
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );

    const loadMore = screen.getByRole("button", { name: "Load more" });
    await userEvent.click(loadMore);

    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    expect(urls.some((url) => url.includes("cursor=c1"))).toBe(true);
  });
});

describe("LeadsScreen — rich create (P-15)", () => {
  it("posts full_name + email + linkedin_url + company_name + source:manual + status:new", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...lead, id: "l-new" }, 201);
      }
      return emptyPage();
    });
    render(<LeadsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Otto Fischer");
    await userEvent.type(screen.getByLabelText("Email"), "otto@example.test");
    await userEvent.type(
      screen.getByLabelText("LinkedIn URL"),
      "https://linkedin.com/in/otto",
    );
    await userEvent.type(screen.getByLabelText("Company"), "Otto Fischer GmbH");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      full_name: "Otto Fischer",
      email: "otto@example.test",
      linkedin_url: "https://linkedin.com/in/otto",
      company_name: "Otto Fischer GmbH",
      source: "manual",
      status: "new",
    });
  });
});

describe("LeadScreen — edit with If-Match (P-1)", () => {
  it("PATCHes /leads/{id} with If-Match:<version> and only the update fields", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetch(async (_url, method, request) => {
      if (method === "PATCH") {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, title: "VP Sales", version: 2 });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    const title = await screen.findByLabelText("Title");
    await userEvent.type(title, "VP Sales");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ title: "VP Sales" });
    expect(patchBody).not.toHaveProperty("status");
    expect(patchBody).not.toHaveProperty("score");
  });

  it("preserves the Promote button and score/status/company badges", async () => {
    stubFetch(async () => jsonResponse(lead));
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    expect(
      screen.getByRole("button", { name: "Promote to contact" }),
    ).toBeTruthy();
    expect(screen.getByText("Score: 72")).toBeTruthy();
    expect(screen.getByText("working")).toBeTruthy();
    expect(screen.getByText("Nordwind Logistik")).toBeTruthy();
  });
});

describe("LeadScreen — disqualify (P-3)", () => {
  it("labels the action Disqualify, DELETEs /leads/{id} on confirm, and navigates to the list", async () => {
    let deleted = false;
    stubFetch(async (url, method) => {
      if (method === "DELETE" && url.includes("/leads/l-1")) {
        deleted = true;
        return jsonResponse({
          ...lead,
          status: "disqualified",
          archived_at: "2026-07-13T00:00:00Z",
        });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByTestId("archive-record")).toBeTruthy(),
    );
    expect(screen.getByTestId("archive-record").textContent).toBe("Disqualify");
    await userEvent.click(screen.getByTestId("archive-record"));
    expect(
      screen.getByText(
        "Are you sure? This disqualifies and archives the lead — there is no undo control.",
      ),
    ).toBeTruthy();
    await userEvent.click(screen.getByTestId("archive-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
    expect(window.location.hash).toBe("#/leads");
  });
});

describe("LeadsScreen — archived marking (P-3)", () => {
  it("shows a Disqualified badge on a row with archived_at set", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [
          {
            ...lead,
            status: "disqualified",
            archived_at: "2026-07-01T00:00:00Z",
          },
        ],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<LeadsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Jonas Petersen")).toBeTruthy(),
    );
    expect(
      screen.getByText("Disqualified", { selector: "span.badge-warn" }),
    ).toBeTruthy();
  });
});

describe("LeadsScreen — dedupe view-existing link (P-16)", () => {
  it("renders a link to the collided record on a duplicate_email 409", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.includes("/leads")) {
        return jsonResponse(
          {
            type: "about:blank",
            title: "Conflict",
            detail: "email already in use",
            code: "duplicate_email",
            details: { existing_id: "01X" },
          },
          409,
        );
      }
      return emptyPage();
    });
    render(<LeadsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Dup Lead");
    await userEvent.type(screen.getByLabelText("Email"), "dup@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.getByText("View existing record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("View existing record"));
    expect(window.location.hash).toBe("#/leads/01X");
  });
});
