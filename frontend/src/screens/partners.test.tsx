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
import { PartnersScreen, PartnerTab } from "./partners";

// Partner tab (company 360) + #/partners list (P-6): the Partner tab treats
// GET /organizations/{id}/partner's 404 as "not a partner yet" (an honest
// empty state + setup form), never as an error; the list reads GET /partners
// with role/cert filters and a row click goes to that org's 360.

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

function stubFetch(
  responder: (
    url: string,
    method: string,
    request: Request,
  ) => Promise<Response>,
) {
  const urls: string[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    urls.push(request.url);
    return responder(request.url, request.method, request);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, urls };
}

const partner = {
  organization_id: "o-1",
  partner_role: "hosting",
  cert_status: "certified",
  margin_tier: "tier2_20",
  relationship_stage: "active",
  next_step: "Renew certification",
  next_step_due_at: "2026-08-01",
  served_segments: ["mid-market"],
  version: 3,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
};

describe("PartnerTab — not yet a partner", () => {
  it("shows the setup form and PUTs the chosen role with no If-Match", async () => {
    let putBody: unknown = null;
    let putHeader: string | null = null;
    stubFetch(async (url, method, request) => {
      if (url.includes("/organizations/o-1/partner") && method === "GET") {
        return jsonResponse({ title: "Not found", detail: "no partner" }, 404);
      }
      if (url.includes("/organizations/o-1/partner") && method === "PUT") {
        putHeader = request.headers.get("If-Match");
        putBody = JSON.parse(await request.text());
        return jsonResponse({ ...partner, cert_status: "applied" });
      }
      throw new Error(`unexpected request ${method} ${url}`);
    });

    render(<PartnerTab organizationId="o-1" />);

    await waitFor(() =>
      expect(screen.getByText("Not a partner yet")).toBeTruthy(),
    );
    await userEvent.selectOptions(
      screen.getByLabelText("Partner role *"),
      "consulting",
    );
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(putBody).toBeTruthy());
    expect(putBody).toMatchObject({ partner_role: "consulting" });
    expect(putHeader).toBeNull();
  });
});

describe("PartnerTab — existing partner", () => {
  it("shows its fields and edits with If-Match", async () => {
    let putHeader: string | null = null;
    let putBody: unknown = null;
    stubFetch(async (url, method, request) => {
      if (url.includes("/organizations/o-1/partner") && method === "GET") {
        return jsonResponse(partner);
      }
      if (url.includes("/organizations/o-1/partner") && method === "PUT") {
        putHeader = request.headers.get("If-Match");
        putBody = JSON.parse(await request.text());
        return jsonResponse({ ...partner, next_step: "Book QBR" });
      }
      throw new Error(`unexpected request ${method} ${url}`);
    });

    render(<PartnerTab organizationId="o-1" />);

    await waitFor(() =>
      expect(screen.getByText("Renew certification")).toBeTruthy(),
    );
    await userEvent.click(screen.getByTestId("edit-partner"));

    const nextStep = await screen.findByLabelText("Next step");
    await userEvent.clear(nextStep);
    await userEvent.type(nextStep, "Book QBR");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(putBody).toBeTruthy());
    expect(putHeader).toBe("3");
    expect(putBody).toMatchObject({ next_step: "Book QBR" });
  });
});

describe("PartnersScreen", () => {
  it("lists partners and sends the role filter", async () => {
    const { urls } = stubFetch(async () =>
      jsonResponse({
        data: [partner],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<PartnersScreen />);

    await waitFor(() => expect(screen.getByText("o-1")).toBeTruthy());
    expect(screen.getByText("Active")).toBeTruthy();

    await userEvent.selectOptions(
      screen.getByLabelText("Partner role"),
      "hosting",
    );

    await waitFor(() =>
      expect(urls.some((url) => url.includes("partner_role=hosting"))).toBe(
        true,
      ),
    );
  });

  it("navigates to the org's 360 on row click", async () => {
    stubFetch(async () =>
      jsonResponse({
        data: [partner],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<PartnersScreen />);

    await waitFor(() => expect(screen.getByText("o-1")).toBeTruthy());
    await userEvent.click(screen.getByText("o-1"));

    expect(window.location.hash).toBe("#/companies/o-1");
  });
});
