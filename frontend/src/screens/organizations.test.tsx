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
import { CompanyScreen } from "./organizations";

// Company-360 enrichment (EP05 scrapeCompany): one click stages a 🟡
// evidence-backed proposal — human field labels, per-field confidence +
// evidence, the confirm-first banner (nothing written until the inbox
// accept), and honest 422 degradation with the server's detail verbatim.

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
