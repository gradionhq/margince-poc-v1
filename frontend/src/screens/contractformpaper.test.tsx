/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ProblemError } from "./common";
import { ContractForm, paperState } from "./contractform";

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

// An empty list is the answer to "there is no paper". It is NOT the answer to
// "still loading", "the read failed", or "your role may not see documents" —
// and rendering a bare drop zone in those three makes the field state an
// absence it has no idea about.
describe("paperState", () => {
  const settled = { isPending: false, isError: false, error: null };

  it("calls a contract being created empty rather than unknown", () => {
    expect(paperState(false, settled, 0)).toBe("empty");
  });

  it("distinguishes no-paper from not-yet-known", () => {
    expect(paperState(true, settled, 0)).toBe("empty");
    expect(paperState(true, { ...settled, isPending: true }, 0)).toBe(
      "loading",
    );
  });

  it("reads a refused document grant as withheld, never as a failure", () => {
    // A retry button on a 403 offers a reader an action that will refuse
    // identically, forever.
    const denied = new ProblemError({ status: 403, code: "permission_denied" });
    expect(
      paperState(true, { isPending: false, isError: true, error: denied }, 0),
    ).toBe("withheld");
  });

  it("reads a server fault as failed, so a retry is offered", () => {
    const broken = new ProblemError({ status: 500, code: "internal" });
    expect(
      paperState(true, { isPending: false, isError: true, error: broken }, 0),
    ).toBe("failed");
    // A dropped connection carries no problem document at all.
    expect(
      paperState(
        true,
        { isPending: false, isError: true, error: new Error("offline") },
        0,
      ),
    ).toBe("failed");
  });

  it("is ready once documents are actually in hand", () => {
    expect(paperState(true, settled, 1)).toBe("ready");
  });
});

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

  it("says the answer is withheld rather than showing an empty field", async () => {
    // 403 on the documents read: this reader's role cannot see them. The
    // contract may well have paper — the field must not imply it does not.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = input instanceof Request ? input.url : String(input);
        if (url.includes("/documents")) {
          return new Response(
            JSON.stringify({ status: 403, code: "permission_denied" }),
            {
              status: 403,
              headers: { "Content-Type": "application/problem+json" },
            },
          );
        }
        return new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );
    show(
      <ContractForm
        orgId="o-1"
        contract={CONTRACT as never}
        open
        onClose={() => {}}
      />,
    );

    expect(
      await screen.findByText("Hidden — your role cannot read this"),
    ).toBeTruthy();
    // And the picker must not restate the absence the panel just declined to
    // claim: "Drop a file here" is the sentence for a field that KNOWS nothing
    // is filed.
    expect(
      screen.queryByText("Drop a file here or click to choose"),
    ).toBeNull();
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
