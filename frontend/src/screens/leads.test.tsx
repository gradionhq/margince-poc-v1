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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  LeadScreen,
  LeadsScreen,
  promoteEligible,
  scoreTone,
  terminalBadge,
} from "./leads";

// The status/score-override/assign-to-me controls (Phase 4) resolve the
// session principal via /v1/me, which needs a workspace slug before it will
// even ask — set on every test, cleared after (mirrors automations.test.tsx).
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

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
  globalThis.localStorage.clear();
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

  it("opening the promote dialog defaults the trigger to human_qualify", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(lead)),
    );
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Promote to contact" }),
    );
    expect(
      (screen.getByLabelText("Promotion trigger") as HTMLSelectElement).value,
    ).toBe("human_qualify");
  });

  it("promote posts the picked trigger + note and lands on the resulting person 360", async () => {
    let promoteBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/leads/l-1/promote")) {
        promoteBody = JSON.parse(await request.text());
        return jsonResponse({ person: anna, merged: false, lead_id: "l-1" });
      }
      return jsonResponse(lead);
    });
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Promote to contact" }),
    );
    await userEvent.selectOptions(
      screen.getByLabelText("Promotion trigger"),
      "meeting_booked",
    );
    await userEvent.type(
      screen.getByLabelText("Evidence note (optional)"),
      "Booked via calendly",
    );
    await userEvent.click(screen.getByRole("button", { name: "Promote" }));
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/p-1"));
    expect(promoteBody).toEqual({
      trigger: "meeting_booked",
      evidence: { note: "Booked via calendly" },
    });
  });

  it("a 409 already_promoted navigates to the existing person instead of erroring", async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method =
          input instanceof Request ? input.method : (init?.method ?? "GET");
        if (method === "POST" && url.includes("/leads/l-1/promote")) {
          return jsonResponse(
            {
              title: "already promoted",
              code: "already_promoted",
              details: { promoted_person_id: "p-9" },
            },
            409,
          );
        }
        return jsonResponse(lead);
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    render(<LeadScreen id="l-1" />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Promote to contact" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Promote" }));
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/p-9"));
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

// A URL-capturing fetch stub that also answers /v1/me — the three Phase-4
// lifecycle controls (status, score override, assign-to-me) all need the
// session principal resolved, so every test below sets a workspace slug and
// serves /v1/me alongside the lead responses.
function stubFetchWithMe(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response | undefined>,
  meId = "u-9",
): { urls: string[] } {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      urls.push(request.url);
      if (request.url.endsWith("/v1/me")) {
        return jsonResponse({
          user: { id: meId, display_name: "Me" },
          roles: ["rep"],
          teams: [],
        });
      }
      const answer = await responder(request.url, request.method, request);
      return answer ?? jsonResponse(lead);
    }),
  );
  return { urls };
}

describe("LeadScreen — status control (P-12)", () => {
  it("shows the status control for a new/working lead and PATCHes status with If-Match", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, status: "working", version: 2 });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Working" })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Working" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ status: "working" });
  });

  it("hides the status control for a promoted/disqualified lead", async () => {
    stubFetchWithMe(async () => jsonResponse({ ...lead, status: "promoted" }));
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    expect(screen.queryByRole("button", { name: "Working" })).toBeNull();
    expect(screen.queryByRole("button", { name: "New" })).toBeNull();
  });
});

describe("LeadScreen — score explain + override (P-10)", () => {
  it("shows Override score for a non-overridden lead; submit requires a reason and PATCHes score + reason", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({
          ...lead,
          score: 90,
          score_override_reason: "Strong buying signal",
          score_computed: 72,
          version: 2,
        });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Override score" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Override score" }),
    );

    const submit = screen.getByRole("button", { name: "Save override" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    const scoreInput = screen.getByLabelText("Score");
    const reasonInput = screen.getByLabelText("Reason");
    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90");
    await userEvent.type(reasonInput, "Strong buying signal");
    expect((submit as HTMLButtonElement).disabled).toBe(false);

    await userEvent.click(submit);

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({
      score: 90,
      score_override_reason: "Strong buying signal",
    });
  });

  it("disables Save override for an out-of-range or non-integer score even with a reason filled in", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, version: 2 });
      }
      return undefined;
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Override score" }),
      ).toBeTruthy(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Override score" }),
    );

    const submit = screen.getByRole("button", { name: "Save override" });
    const scoreInput = screen.getByLabelText("Score");
    const reasonInput = screen.getByLabelText("Reason");
    await userEvent.type(reasonInput, "Strong buying signal");

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "150");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "-5");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90.5");
    expect((submit as HTMLButtonElement).disabled).toBe(true);

    await userEvent.clear(scoreInput);
    await userEvent.type(scoreInput, "90");
    expect((submit as HTMLButtonElement).disabled).toBe(false);

    await userEvent.click(submit);
    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ score: 90 });
  });

  it("shows the reason + machine value and Clear override for an overridden lead", async () => {
    let patchBody: unknown = null;
    const overridden = {
      ...lead,
      score: 90,
      score_override_reason: "Strong buying signal",
      score_computed: 72,
    };
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, score: 72, version: 2 });
      }
      return jsonResponse(overridden);
    });
    render(<LeadScreen id="l-1" />);

    await waitFor(() =>
      expect(screen.getByText(/Strong buying signal/)).toBeTruthy(),
    );
    expect(screen.getByText(/72/)).toBeTruthy();
    const clear = screen.getByRole("button", { name: "Clear override" });
    await userEvent.click(clear);

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ score: null });
  });
});

describe("LeadScreen — owner display + assign to me (P-11)", () => {
  it("shows Unassigned and Assign to me PATCHes owner_id to the current user", async () => {
    let patchBody: unknown = null;
    stubFetchWithMe(async (url, method, request) => {
      if (method === "PATCH" && url.includes("/leads/l-1")) {
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...lead, owner_id: "u-9", version: 2 });
      }
      return undefined;
    }, "u-9");
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByText("Unassigned")).toBeTruthy());
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Assign to me" })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Assign to me" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchBody).toMatchObject({ owner_id: "u-9" });
  });

  it("hides Assign to me when the lead is already owned by the current user", async () => {
    const { urls } = stubFetchWithMe(
      async () => jsonResponse({ ...lead, owner_id: "u-9" }),
      "u-9",
    );
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByText(/u-9/)).toBeTruthy());
    // Wait past the me-probe resolving too, so the assertion below isn't a
    // false negative from the button not having had a chance to render yet.
    await waitFor(() =>
      expect(urls.some((url) => url.endsWith("/v1/me"))).toBe(true),
    );
    expect(screen.queryByRole("button", { name: "Assign to me" })).toBeNull();
  });
});

describe("terminalBadge (archived/terminal labelling)", () => {
  it("labels disqualified and promoted distinctly and leaves open leads unbadged", () => {
    expect(terminalBadge("disqualified")).toEqual({
      label: "lead.disqualified",
      tone: "warn",
    });
    // A promoted lead IS archived, but reads "Archived" — never "Disqualified".
    expect(terminalBadge("promoted")).toEqual({
      label: "record.archived",
      tone: "warn",
    });
    expect(terminalBadge("new")).toBeNull();
    expect(terminalBadge("working")).toBeNull();
  });
});

describe("LeadScreen — archived/terminal is read-only (P-3)", () => {
  it("a promoted lead reads Archived (not Disqualified) and hides edit/disqualify/promote/override", async () => {
    stubFetchWithMe(async () =>
      jsonResponse({
        ...lead,
        status: "promoted",
        archived_at: "2026-07-13T00:00:00Z",
      }),
    );
    render(<LeadScreen id="l-1" />);

    await waitFor(() => expect(screen.getByText("Archived")).toBeTruthy());
    expect(screen.queryByText("Disqualified")).toBeNull();
    expect(screen.queryByTestId("edit-record")).toBeNull();
    expect(screen.queryByTestId("archive-record")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Promote to contact" }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Override score" })).toBeNull();
  });

  it("shows an 'overridden' badge when the score is human-overridden", async () => {
    stubFetchWithMe(async () =>
      jsonResponse({
        ...lead,
        score_override_reason: "Strong buying signal",
        score_computed: 50,
      }),
    );
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByText("overridden")).toBeTruthy());
  });

  it("names the owner 'you' when the lead is owned by the current user", async () => {
    stubFetchWithMe(
      async () => jsonResponse({ ...lead, owner_id: "u-9" }),
      "u-9",
    );
    render(<LeadScreen id="l-1" />);
    await waitFor(() => expect(screen.getByText("Owner: you")).toBeTruthy());
  });
});
