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
import { ContactsScreen, PersonScreen } from "./people";

// B-EP09.10a acceptance: per-row provenance chips, row→360 navigation, and
// the honest error state. Lead-specific acceptance (score thresholds,
// promote eligibility, the §3.5 segregated LeadsScreen/LeadScreen) lives in
// leads.test.tsx.

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
  title: "Head of Procurement",
  emails: [{ id: "e-1", email: "anna.weber@brandt.example", is_primary: true }],
  captured_by: "connector:gmail",
  source: "gmail",
  version: 1,
};

const employmentRel = {
  id: "rel-1",
  workspace_id: "w-1",
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

describe("ContactsScreen (B-EP09.10a)", () => {
  it("renders rows with provenance chips and navigates to the person 360", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ data: [anna], page: { next_cursor: null } }),
      ),
    );
    render(<ContactsScreen />);
    await waitFor(() => expect(screen.getByText("Anna Weber")).toBeTruthy());
    expect(screen.getByText("agent: connector:gmail")).toBeTruthy();
    await userEvent.click(screen.getByText("Anna Weber"));
    expect(window.location.hash).toBe("#/contacts/p-1");
  });

  it("renders the honest error state with the RFC7807 detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            type: "about:blank",
            title: "Forbidden",
            detail: "missing scope people:read",
          },
          403,
        ),
      ),
    );
    render(<ContactsScreen />);
    await waitFor(() =>
      expect(screen.getByText("Couldn't load this view.")).toBeTruthy(),
    );
    expect(screen.getByText("missing scope people:read")).toBeTruthy();
  });
});

// The dormant/no-interactions strength response — the default backstop for
// every stubFetch call below that isn't itself exercising the strength card
// (P-4): the Person Overview now fires this GET unconditionally, and none of
// those pre-existing tests care about its shape, so they get an honest
// zero/dormant reading rather than a mismatched shape from the person-fixture
// catch-all.
const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

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
        anchor: { type: "person", id: "p-1" },
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

describe("ContactsScreen — search/sort/pagination (P-14)", () => {
  it("carries the debounced search term into the next fetch", async () => {
    const { urls } = stubFetch(async () => emptyPage());
    render(<ContactsScreen />);
    await waitFor(() => expect(urls.length).toBeGreaterThan(0));

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByRole("searchbox"), {
        target: { value: "anna" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() =>
      expect(urls.some((url) => url.includes("q=anna"))).toBe(true),
    );
  });

  it("shows Load more on has_more and fetches the next cursor on click", async () => {
    const { urls } = stubFetch(async (url) => {
      if (url.includes("cursor=c1")) {
        return jsonResponse({
          data: [{ ...anna, id: "p-2", full_name: "Otto Fischer" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      return jsonResponse({
        data: [anna],
        page: { next_cursor: "c1", has_more: true },
      });
    });
    render(<ContactsScreen />);
    await waitFor(() => expect(screen.getByText("Anna Weber")).toBeTruthy());

    const loadMore = screen.getByRole("button", { name: "Load more" });
    await userEvent.click(loadMore);

    await waitFor(() => expect(screen.getByText("Otto Fischer")).toBeTruthy());
    expect(urls.some((url) => url.includes("cursor=c1"))).toBe(true);
  });
});

describe("ContactsScreen — rich create (P-15)", () => {
  it("shows repeatable emails/phones, title, and a linkedin field", async () => {
    stubFetch(async () => emptyPage());
    render(<ContactsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    expect(screen.getByLabelText("Title")).toBeTruthy();
    expect(screen.getByLabelText("LinkedIn")).toBeTruthy();
    expect(screen.getByText("Add email")).toBeTruthy();
    expect(screen.getByText("Add phone")).toBeTruthy();
  });

  // Regression: the email/phone "Type" select's options are keyed messages
  // (field.emailWork/…) resolved by contactCreateFields via useT() — the
  // rendered option text must be the translated word, never the raw
  // MessageKey string (fieldControl in create.tsx renders option.label
  // verbatim, so an untranslated key would leak straight to the DOM).
  it("shows translated Type option text, not the raw i18n key", async () => {
    stubFetch(async () => emptyPage());
    render(<ContactsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.click(screen.getByText("Add phone"));
    const [emailType, phoneType] = screen.getAllByLabelText("Type");

    const emailOptionText = within(emailType as HTMLElement)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(emailOptionText).toEqual(["", "Work", "Personal", "Other"]);
    expect(emailOptionText).not.toContain("field.emailWork");

    const phoneOptionText = within(phoneType as HTMLElement)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(phoneOptionText).toEqual(["", "Work", "Mobile", "Home", "Other"]);
    expect(phoneOptionText).not.toContain("field.phoneWork");
  });

  it("shows German Type option text under the de locale", async () => {
    stubFetch(async () => emptyPage());
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="de">
          <ContactsScreen />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.click(screen.getByText("E-Mail hinzufügen"));
    const emailType = screen.getByLabelText("Typ");
    const optionText = within(emailType)
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(optionText).toEqual(["", "Geschäftlich", "Privat", "Sonstige"]);
  });

  it("posts full_name + emails + source:manual on submit", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/people")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...anna, id: "p-new" }, 201);
      }
      return emptyPage();
    });
    render(<ContactsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Otto Fischer");
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.type(screen.getByLabelText("Email *"), "otto@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      full_name: "Otto Fischer",
      source: "manual",
      emails: [
        {
          email: "otto@example.test",
          email_type: "work",
          is_primary: false,
          position: 0,
        },
      ],
    });
  });
});

describe("PersonScreen — edit with If-Match (P-1)", () => {
  it("PATCHes /people/{id} with If-Match:<version> and the changed field", async () => {
    let patchHeader: string | null = null;
    let patchBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "PATCH") {
        patchHeader = request.headers.get("If-Match");
        patchBody = JSON.parse(await request.text());
        return jsonResponse({ ...anna, title: "New title", version: 2 });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    const title = await screen.findByLabelText("Title");
    await userEvent.clear(title);
    await userEvent.type(title, "New title");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patchBody).toBeTruthy());
    expect(patchHeader).toBe("1");
    expect(patchBody).toMatchObject({ title: "New title" });
  });

  it("shows the friendly version-skew copy on a 409 code:version_skew, not the raw detail", async () => {
    stubFetch(async (url, method) => {
      if (method === "PATCH") {
        return jsonResponse(
          {
            type: "about:blank",
            title: "Conflict",
            detail: "if-match version 1 does not match current version 2",
            code: "version_skew",
          },
          409,
        );
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByTestId("edit-record")).toBeTruthy());
    await userEvent.click(screen.getByTestId("edit-record"));
    const title = await screen.findByLabelText("Title");
    await userEvent.clear(title);
    await userEvent.type(title, "New title");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        screen.getByText(
          "This record changed since you opened it — reload and try again.",
        ),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByText("if-match version 1 does not match current version 2"),
    ).toBeNull();
  });
});

describe("PersonScreen — archive (P-3)", () => {
  it("opens a confirm, DELETEs /people/{id} on confirm, and navigates to the list", async () => {
    let deleted = false;
    stubFetch(async (url, method) => {
      if (method === "DELETE" && url.includes("/people/p-1")) {
        deleted = true;
        return jsonResponse({ ...anna, archived_at: "2026-07-13T00:00:00Z" });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() =>
      expect(screen.getByTestId("archive-record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("archive-record"));
    expect(
      screen.getByText(
        "Are you sure? This archives the record — there is no undo control.",
      ),
    ).toBeTruthy();
    await userEvent.click(screen.getByTestId("archive-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
    expect(window.location.hash).toBe("#/contacts");
  });
});

describe("ContactsScreen — archived marking (P-3)", () => {
  it("shows an Archived badge on a row with archived_at set", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [{ ...anna, archived_at: "2026-07-01T00:00:00Z" }],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<ContactsScreen />);
    await waitFor(() => expect(screen.getByText("Anna Weber")).toBeTruthy());
    expect(screen.getByText("Archived")).toBeTruthy();
  });
});

describe("ContactsScreen — dedupe view-existing link (P-16)", () => {
  it("renders a link to the collided record on a duplicate_email 409", async () => {
    stubFetch(async (url, method) => {
      if (method === "POST" && url.includes("/people")) {
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
    render(<ContactsScreen />);
    await userEvent.click(screen.getByTestId("new-record"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Dup Person");
    await userEvent.click(screen.getByText("Add email"));
    await userEvent.type(screen.getByLabelText("Email *"), "dup@example.test");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.getByText("View existing record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("View existing record"));
    expect(window.location.hash).toBe("#/contacts/01X");
  });
});

describe("PersonScreen — merge into target (P-2)", () => {
  const otto = { ...anna, id: "p-2", full_name: "Otto Fischer" };

  it("searches, excludes the source row, and merges into the picked target", async () => {
    let mergeBody: unknown = null;
    let mergeHeader: string | null = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/people/p-1/merge")) {
        mergeHeader = request.headers.get("If-Match");
        mergeBody = JSON.parse(await request.text());
        return jsonResponse({ ...otto, version: 2 });
      }
      if (url.includes("/people?") && url.includes("q=otto")) {
        return jsonResponse({
          data: [anna, otto],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() =>
      expect(screen.getByTestId("merge-record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("merge-record"));
    await userEvent.type(screen.getByPlaceholderText("Search…"), "otto");

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
      expect(within(dialog).getByText("Otto Fischer")).toBeTruthy(),
    );
    // The source row must never appear as a mergeable target.
    expect(within(dialog).queryByText("Anna Weber")).toBeNull();

    await userEvent.click(within(dialog).getByText("Otto Fischer"));
    await userEvent.click(screen.getByTestId("merge-confirm"));

    await waitFor(() => expect(mergeBody).toBeTruthy());
    expect(mergeBody).toMatchObject({ target_id: "p-2" });
    expect(mergeHeader).toBe("1");
    expect(window.location.hash).toBe("#/contacts/p-2");
  });

  it("shows a search error instead of an unhandled rejection when the target search fails", async () => {
    stubFetch(async (url) => {
      if (url.includes("/people?") && url.includes("q=otto")) {
        return jsonResponse(
          { type: "about:blank", title: "server error", detail: "boom" },
          500,
        );
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() =>
      expect(screen.getByTestId("merge-record")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("merge-record"));
    await userEvent.type(screen.getByPlaceholderText("Search…"), "otto");

    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    const dialog = screen.getByRole("dialog");
    await waitFor(() => expect(within(dialog).getByText("boom")).toBeTruthy());
  });
});

describe("PersonScreen — Relationships tab (P-5)", () => {
  it("shows an Overview/Relationships tab bar and lists relationships by person_id", async () => {
    stubFetch(async (url) => {
      if (url.includes("/relationships") && url.includes("person_id=p-1")) {
        return jsonResponse({
          data: [employmentRel],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));

    await waitFor(() => expect(screen.getByText("Employment")).toBeTruthy());
    expect(screen.getByText("cto")).toBeTruthy();
    expect(screen.getByText("o-1")).toBeTruthy();
  });

  it("adding a relationship POSTs /relationships with the scope id + kind + source", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/relationships")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...employmentRel, id: "rel-new" }, 201);
      }
      if (url.includes("/relationships") && url.includes("person_id=p-1")) {
        return emptyPage();
      }
      if (url.includes("/organizations?") && url.includes("q=brandt")) {
        return jsonResponse({
          data: [{ id: "o-1", display_name: "Brandt Automotive GmbH" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));
    await waitFor(() =>
      expect(screen.getByTestId("add-relationship")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("add-relationship"));

    await userEvent.type(screen.getByPlaceholderText("Search…"), "brandt");
    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() =>
      expect(screen.getByText("Brandt Automotive GmbH")).toBeTruthy(),
    );
    await userEvent.click(screen.getByText("Brandt Automotive GmbH"));
    await userEvent.click(screen.getByTestId("add-relationship-submit"));

    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      person_id: "p-1",
      organization_id: "o-1",
      kind: "employment",
      source: "manual",
    });
  });

  it("removing a relationship calls DELETE /relationships/{id}", async () => {
    let deleted = false;
    stubFetch(async (url, method) => {
      if (method === "DELETE" && url.includes("/relationships/rel-1")) {
        deleted = true;
        return jsonResponse({
          ...employmentRel,
          archived_at: "2026-07-13T00:00:00Z",
        });
      }
      if (url.includes("/relationships") && url.includes("person_id=p-1")) {
        return jsonResponse({
          data: [employmentRel],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));
    await waitFor(() =>
      expect(screen.getByTestId("remove-relationship")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("remove-relationship"));
    await waitFor(() =>
      expect(screen.getByTestId("remove-relationship-confirm")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("remove-relationship-confirm"));

    await waitFor(() => expect(deleted).toBe(true));
  });
});

describe("PersonScreen — relationship-strength card (P-4)", () => {
  it("renders the bucket badge, score, and all four factor labels", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(anna);
      },
      {
        strength: {
          score: 72,
          bucket: "strong",
          factors: {
            recency: 0.9,
            frequency: 0.6,
            reciprocity: 0.5,
            direction: 0.8,
          },
          last_interaction: "2026-07-01T09:00:00Z",
          inbound_90d: 5,
          outbound_90d: 7,
          contributing_activity_ids: ["a-1", "a-2", "a-3"],
        },
      },
    );
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByText("Strong")).toBeTruthy());
    expect(screen.getByText("Score 72/100")).toBeTruthy();
    expect(screen.getByText("Recency")).toBeTruthy();
    expect(screen.getByText("Frequency")).toBeTruthy();
    expect(screen.getByText("Reciprocity")).toBeTruthy();
    expect(screen.getByText("Direction")).toBeTruthy();
    expect(screen.getByText("90%")).toBeTruthy();
    expect(screen.getByText("5 in · 7 out (90d)")).toBeTruthy();
    expect(screen.getByText("Computed from 3 activities")).toBeTruthy();
  });

  it("renders an honest 'no interactions yet' state for a dormant/score-0 record", async () => {
    stubFetch(
      async (url) => {
        if (url.includes("/activities")) {
          return jsonResponse({ data: [] });
        }
        return jsonResponse(anna);
      },
      { strength: dormantStrength },
    );
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByText("Dormant")).toBeTruthy());
    expect(screen.getByText("Score 0/100")).toBeTruthy();
    expect(screen.getByText("No interactions yet")).toBeTruthy();
    expect(screen.queryByText(/^0$/)).toBeNull();
  });
});

describe("PersonScreen — archived is read-only (P-3)", () => {
  it("hides edit/merge/archive and shows the Archived badge on an archived person", async () => {
    stubFetch(async (url) => {
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse({ ...anna, archived_at: "2026-07-13T00:00:00Z" });
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() => expect(screen.getByText("Archived")).toBeTruthy());
    expect(screen.queryByTestId("edit-record")).toBeNull();
    expect(screen.queryByTestId("merge-record")).toBeNull();
    expect(screen.queryByTestId("archive-record")).toBeNull();
  });
});

describe("PersonScreen — relationship kinds by scope (P-5)", () => {
  it("offers deal_stakeholder (not org↔org) from a person, searches deals, confirms, and POSTs deal_id", async () => {
    let posted: unknown = null;
    stubFetch(async (url, method, request) => {
      if (method === "POST" && url.includes("/relationships")) {
        posted = JSON.parse(await request.text());
        return jsonResponse({ ...employmentRel, id: "rel-new" }, 201);
      }
      if (url.includes("/relationships") && url.includes("person_id=p-1")) {
        return emptyPage();
      }
      if (url.includes("/deals")) {
        return jsonResponse({
          data: [{ id: "d-1", name: "Q3 Renewal" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy());
    await userEvent.click(screen.getByText("Relationships"));
    await waitFor(() =>
      expect(screen.getByTestId("add-relationship")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("add-relationship"));

    // A person can anchor employment + deal_stakeholder; the org↔org kinds
    // (partner_of/…) need two orgs and must not be offered here.
    const kind = screen.getByLabelText("Kind");
    expect(within(kind).queryByText("Partner of")).toBeNull();
    await userEvent.selectOptions(kind, "deal_stakeholder");

    await userEvent.type(screen.getByPlaceholderText("Search…"), "q3");
    vi.useFakeTimers();
    try {
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() => expect(screen.getByText("Q3 Renewal")).toBeTruthy());
    await userEvent.click(screen.getByText("Q3 Renewal"));

    // A meaningful confirmation after select, consistent with merge.
    expect(
      screen.getByText("Add a Deal stakeholder link to Q3 Renewal."),
    ).toBeTruthy();

    await userEvent.click(screen.getByTestId("add-relationship-submit"));
    await waitFor(() => expect(posted).toBeTruthy());
    expect(posted).toMatchObject({
      person_id: "p-1",
      deal_id: "d-1",
      kind: "deal_stakeholder",
      source: "manual",
    });
    expect(posted).not.toHaveProperty("organization_id");
  });
});

describe("PersonScreen — History tab", () => {
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
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /history/i })).toBeTruthy(),
    );
    await userEvent.click(screen.getByRole("button", { name: /history/i }));

    await waitFor(() =>
      expect(screen.getByText("Created the record")).toBeTruthy(),
    );
  });
});

// consent.test.tsx covers ConsentSection's own behaviour exhaustively; what
// it can't see is whether the Person 360 actually renders the component at
// all. It didn't, once — an extraction (consent.tsx, pulled out of this
// file) can compile clean and pass every existing suite while quietly
// leaving the caller's JSX without the import it needs.
describe("PersonScreen — consent section wiring", () => {
  it("renders the Art. 7 consent card on the overview tab", async () => {
    stubFetch(async (url) => {
      if (url.includes("/consent-purposes")) {
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (url.includes("/consent")) {
        return jsonResponse({ state: [], events: [] });
      }
      if (url.includes("/activities")) {
        return jsonResponse({ data: [] });
      }
      return jsonResponse(anna);
    });
    render(<PersonScreen id="p-1" />);

    // The section's own aria-label gives it an implicit region role — an
    // absent import would leave no such region on the page at all.
    expect(await screen.findByRole("region", { name: "Consent" })).toBeTruthy();
  });
});
