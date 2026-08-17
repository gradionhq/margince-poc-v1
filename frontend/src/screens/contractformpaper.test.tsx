/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ContractForm } from "./contractform";

// The signed PDF has to be reachable from the form a reader lands on.
//
// Clicking a contract opens this form, so a form that offers only an upload
// tells the reader there is no paper on file — which is what it did: the field
// knew about a newly picked File and never asked what was already filed.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const CONTRACT = {
  id: "c-1",
  organization_id: "o-1",
  title: "valantic GmbH — Rahmenvertrag",
  source: "manual",
  captured_by: "human:u-1",
  status: "active",
  under_contract: true,
  auto_renew: false,
  value_basis: "annualized_12m",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const PAPER = {
  id: "a-9",
  filename: "V-5253-VALA.pdf",
  title: "valantic GmbH — Rahmenvertrag",
  category: "contract",
  doc_state: "current",
  pinned: false,
  created_at: "2026-01-02T09:00:00Z",
  entity_type: "organization",
  entity_id: "o-1",
  contract_id: "c-1",
  source: "upload",
  captured_by: "human:u-1",
};

// The generated client calls fetch with a Request, whose String() is
// "[object Request]" — reading `.url` is what actually names the path.
function stub(docs: unknown[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = input instanceof Request ? input.url : String(input);
      const body = url.includes("/documents") ? { data: docs } : { data: [] };
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

function show(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("the signed document on the contract form", () => {
  it("offers the filed PDF as a download", async () => {
    stub([PAPER]);
    show(
      <ContractForm
        orgId="o-1"
        contract={CONTRACT as never}
        open
        onClose={() => {}}
      />,
    );

    const link = await screen.findByRole("link", {
      name: "valantic GmbH — Rahmenvertrag",
    });
    expect(link.getAttribute("href")).toBe("/v1/attachments/a-9");
    // The saved file keeps the name it was uploaded under, not the display
    // title the agreement carries.
    expect(link.getAttribute("download")).toBe("V-5253-VALA.pdf");
  });

  it("asks for nothing when the agreement is being created", async () => {
    const asked: string[] = [];
    const fetched = vi.fn(async (input: RequestInfo | URL) => {
      asked.push(input instanceof Request ? input.url : String(input));
      return new Response(JSON.stringify({ data: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetched);
    // No contract: there is no id yet, so asking for "this agreement's
    // documents" would be a request about a record that does not exist.
    show(<ContractForm orgId="o-1" open onClose={() => {}} />);

    await screen.findByText("Record an agreement");
    await waitFor(() => {
      expect(asked.filter((url) => url.includes("/documents"))).toHaveLength(0);
    });
  });
});
