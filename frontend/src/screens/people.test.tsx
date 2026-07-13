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
