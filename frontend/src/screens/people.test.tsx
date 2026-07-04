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
import { LeadScreen, LeadsScreen, promoteEligible, scoreTone } from "./leads";
import { ContactsScreen } from "./people";

// B-EP09.10a/b acceptance: per-row provenance chips, row→360 navigation
// targets (lead rows go to the LEAD detail, never the person screen —
// gap §3.5), the ≥60/40–59/<40 score thresholds, eligibility-gated promote,
// and the honest error state.

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
