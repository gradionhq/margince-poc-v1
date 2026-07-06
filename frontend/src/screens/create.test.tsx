/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { DealsScreen } from "./deals";
import { ContactsScreen } from "./people";

// Create flows (the "you can actually add a record" acceptance): the list
// screens open a create modal, the POST body carries the server's shape
// (source stamped manual, emails as typed rows, major→minor amount), a
// success navigates to the fresh 360, and a 422 renders its RFC 7807 detail
// verbatim — the server's validation is the truth.

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

const emptyPage = { data: [], page: { next_cursor: null } };

type Captured = { key: string; body: unknown };

function stubApi(
  routes: Record<string, (body: unknown) => Response>,
  captured?: Captured[],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      captured?.push({ key, body });
      const handler = routes[key];
      return handler ? handler(body) : jsonResponse(emptyPage);
    }),
  );
}

const pipeline = {
  id: "pl",
  workspace_id: "w",
  name: "Sales",
  is_default: true,
  position: 0,
  stages: [
    {
      id: "s1",
      workspace_id: "w",
      pipeline_id: "pl",
      name: "Qualify",
      position: 1,
      semantic: "open",
      win_probability: 20,
    },
    {
      id: "s4",
      workspace_id: "w",
      pipeline_id: "pl",
      name: "Won",
      position: 4,
      semantic: "won",
      win_probability: 100,
    },
  ],
};

describe("contact create flow", () => {
  it("posts the typed values with source=manual and navigates to the new 360", async () => {
    const captured: Captured[] = [];
    stubApi(
      {
        "POST /people": (body) =>
          jsonResponse(
            {
              id: "p-new",
              workspace_id: "w",
              full_name: (body as { full_name: string }).full_name,
              captured_by: "human:u1",
              source: "manual",
              version: 1,
            },
            201,
          ),
      },
      captured,
    );
    render(<ContactsScreen />);
    await userEvent.click(screen.getByText("New contact"));
    await userEvent.type(screen.getByLabelText("Full name *"), "Peter Neu");
    await userEvent.type(screen.getByLabelText("Email"), "peter@neu.example");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(window.location.hash).toBe("#/contacts/p-new"));
    const post = captured.find((entry) => entry.key === "POST /people");
    expect(post?.body).toMatchObject({
      full_name: "Peter Neu",
      source: "manual",
      emails: [
        { email: "peter@neu.example", email_type: "work", is_primary: true },
      ],
    });
  });

  it("renders the server's 422 detail verbatim and stays open", async () => {
    stubApi({
      "POST /people": () =>
        jsonResponse(
          { title: "Unprocessable", detail: "full_name must not be blank" },
          422,
        ),
    });
    render(<ContactsScreen />);
    await userEvent.click(screen.getByText("New contact"));
    await userEvent.type(screen.getByLabelText("Full name *"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(screen.getByText("full_name must not be blank")).toBeTruthy(),
    );
    expect(screen.getByLabelText("Full name *")).toBeTruthy();
  });
});

describe("deal create flow", () => {
  it("offers only open stages, converts major→minor, and posts the pipeline", async () => {
    const captured: Captured[] = [];
    stubApi(
      {
        "GET /pipelines": () =>
          jsonResponse({ data: [pipeline], page: { next_cursor: null } }),
        "POST /deals": (body) =>
          jsonResponse(
            {
              id: "d-new",
              workspace_id: "w",
              name: (body as { name: string }).name,
              pipeline_id: "pl",
              stage_id: "s1",
              status: "open",
              source: "manual",
              captured_by: "human:u1",
              version: 1,
              created_at: "2026-07-06T08:00:00Z",
              updated_at: "2026-07-06T08:00:00Z",
            },
            201,
          ),
      },
      captured,
    );
    render(<DealsScreen startCreating />);
    await waitFor(() => expect(screen.getByLabelText("Stage *")).toBeTruthy());
    const stageSelect = screen.getByLabelText("Stage *") as HTMLSelectElement;
    // won/lost stages are not creatable targets — deals are born open
    expect(
      Array.from(stageSelect.options).map((option) => option.textContent),
    ).toEqual(["Qualify"]);
    await userEvent.type(screen.getByLabelText("Deal name *"), "Neuer Deal");
    await userEvent.type(screen.getByLabelText("Value"), "480");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(window.location.hash).toBe("#/deals/d-new"));
    const post = captured.find((entry) => entry.key === "POST /deals");
    expect(post?.body).toMatchObject({
      name: "Neuer Deal",
      amount_minor: 48000,
      currency: "EUR",
      pipeline_id: "pl",
      stage_id: "s1",
      source: "manual",
    });
  });
});
