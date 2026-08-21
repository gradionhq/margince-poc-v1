/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ProblemError } from "./common";
import { ContractForm } from "./contractform";
import { paperState } from "./contractpaper";

// The signed PDF has to be reachable from the form a reader lands on.
//
// Clicking a contract opens this form, so a form that offers only an upload
// tells the reader there is no paper on file — which is what it did: the field
// knew about a newly picked File and never asked what was already filed.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const CONTRACT: components["schemas"]["Contract"] = {
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

const PAPER: components["schemas"]["Attachment"] = {
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

// A documents endpoint that PAGINATES, answered page by page off the `cursor`
// the client sends back. Routing on the cursor rather than on a call counter is
// what makes the assertion mean something: a field that ignored `next_cursor`
// would never reach the second page, and a counter would hand it that page
// anyway.
function stubPages(pages: { docs: unknown[]; next: string | null }[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(
        input instanceof Request ? input.url : String(input),
        "http://x",
      );
      if (!url.pathname.includes("/documents")) {
        return new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      const cursor = url.searchParams.get("cursor");
      const index = cursor === null ? 0 : Number(cursor);
      const page = pages[index];
      if (!page) {
        throw new Error(
          `the field walked past the last page (cursor ${cursor})`,
        );
      }
      return new Response(
        JSON.stringify({
          data: page.docs,
          page: { has_more: page.next !== null, next_cursor: page.next },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
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
  // `remaining: 0` is the read that reached the END of the list — the only
  // shape a surface may present as the whole picture.
  const WHOLE_LIST = { documents: [], remaining: 0 };

  it("calls a contract being created empty rather than unknown", () => {
    expect(paperState(false, settled, undefined)).toBe("empty");
  });

  it("distinguishes no-paper from not-yet-known", () => {
    expect(paperState(true, settled, WHOLE_LIST)).toBe("empty");
    expect(paperState(true, { ...settled, isPending: true }, undefined)).toBe(
      "loading",
    );
  });

  it("reads a refused document grant as withheld, never as a failure", () => {
    // A retry button on a 403 offers a reader an action that will refuse
    // identically, forever.
    const denied = new ProblemError({ status: 403, code: "permission_denied" });
    expect(
      paperState(
        true,
        { isPending: false, isError: true, error: denied },
        undefined,
      ),
    ).toBe("withheld");
  });

  it("reads a server fault as failed, so a retry is offered", () => {
    const broken = new ProblemError({ status: 500, code: "internal" });
    expect(
      paperState(
        true,
        { isPending: false, isError: true, error: broken },
        undefined,
      ),
    ).toBe("failed");
    // A dropped connection carries no problem document at all.
    expect(
      paperState(
        true,
        { isPending: false, isError: true, error: new Error("offline") },
        undefined,
      ),
    ).toBe("failed");
    // `typeof null === "object"`, so a null body reaches the property read.
    // Deciding how to REPORT a failure must not itself throw.
    expect(
      paperState(
        true,
        { isPending: false, isError: true, error: new ProblemError(null) },
        undefined,
      ),
    ).toBe("failed");
  });

  it("is ready once documents are actually in hand", () => {
    expect(
      paperState(true, settled, { documents: [PAPER], remaining: 0 }),
    ).toBe("ready");
  });

  // The whole point of #1549: a page is not a list. `ready` is the field
  // saying "this is the paper on file", so a read that stopped short of the
  // end may not borrow it — not with a counted remainder, and not when the
  // bounded count never reached the end either.
  it("refuses to call a truncated page ready", () => {
    expect(
      paperState(true, settled, { documents: [PAPER], remaining: 13 }),
    ).toBe("partial");
    expect(paperState(true, settled, { documents: [PAPER] })).toBe("partial");
  });
});

describe("the signed document on the contract form", () => {
  it("offers the filed PDF as a download", async () => {
    stub([PAPER]);
    show(
      <ContractForm orgId="o-1" contract={CONTRACT} open onClose={() => {}} />,
    );

    const link = await screen.findByRole("link", {
      name: "valantic GmbH — Rahmenvertrag",
    });
    expect(link.getAttribute("href")).toBe("/v1/attachments/a-9");
    // The saved file keeps the name it was uploaded under, not the display
    // title the agreement carries.
    expect(link.getAttribute("download")).toBe("V-5253-VALA.pdf");
  });

  // #1549: the endpoint paginates, and a page presented as a list is the field
  // telling the reader "this is the paper on file" about paper it never asked
  // for.
  it("says how much paper it is not showing", async () => {
    const shown = [
      PAPER,
      { ...PAPER, id: "a-10", filename: "annex-a.pdf", title: "Annex A" },
    ];
    stubPages([
      { docs: shown, next: "1" },
      {
        docs: [
          { ...PAPER, id: "a-11" },
          { ...PAPER, id: "a-12" },
        ],
        next: null,
      },
    ]);
    show(
      <ContractForm orgId="o-1" contract={CONTRACT} open onClose={() => {}} />,
    );

    // The page it holds is still shown — a truncation notice is not a reason to
    // withhold the documents the read did reach.
    expect(await screen.findByRole("link", { name: "Annex A" })).toBeTruthy();
    // And the remainder is COUNTED, from the pages the walk read past the
    // first: two documents the field is not showing, said in words.
    expect(await screen.findByText("2 more not shown")).toBeTruthy();
  });

  // The bound is the other half of the honesty: a library deeper than the walk
  // is still partial, and the field says so WITHOUT a number, because the
  // endpoint publishes no total and a figure here would be invented.
  it("stays partial without a count when the remainder outruns the walk", async () => {
    // Every page says there is another. The walk stops at its own bound.
    stubPages(
      Array.from({ length: 8 }, (_, index) => ({
        docs: [{ ...PAPER, id: `a-${index}` }],
        next: String(index + 1),
      })),
    );
    show(
      <ContractForm orgId="o-1" contract={CONTRACT} open onClose={() => {}} />,
    );

    expect(await screen.findByText("Showing part of the list")).toBeTruthy();
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
      <ContractForm orgId="o-1" contract={CONTRACT} open onClose={() => {}} />,
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
