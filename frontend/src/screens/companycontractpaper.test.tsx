/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompanyContractsCard } from "./companycontracts";

// The signed PDF belongs on the row for the agreement it covers.
//
// A company with a 2024 and a 2026 framework agreement has two files whose
// names differ by one digit, so the card asks for paper by the `contract_id`
// the upload filed rather than matching on text — a title match would hand a
// reader the wrong contract with full confidence.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const CONTRACT = {
  id: "c-1",
  organization_id: "o-1",
  title: "Framework agreement 2026",
  contract_number: "GR-2026-0092",
  source: "manual",
  captured_by: "human:u-1",
  status: "active",
  under_contract: true,
  auto_renew: false,
  value_basis: "total",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const PAPER = {
  id: "a-1",
  filename: "GR-2026-0092.pdf",
  title: "Framework agreement 2026",
  category: "contract",
  doc_state: "final",
  pinned: false,
  created_at: "2026-01-02T09:00:00Z",
  entity_type: "organization",
  entity_id: "o-1",
  contract_id: "c-1",
  source: "upload",
  captured_by: "human:u-1",
};

// The card reads contract:read before it lists anything, so a reader without
// the grant sees WITHHELD rather than an empty account.
// `user` is required on MeResponse — useMe treats a payload without it as a
// server answering garbage, so a fixture that omitted it would fail every
// grant check for a reason that has nothing to do with grants.
const GRANTED = {
  user: { id: "u-1", email: "rep@example.com" },
  authorization: {
    objects: { contract: { read: true, update: true, delete: true } },
  },
};

// Routes by path so the card's reads — its grants, its contracts, and each
// row's paper — are answered independently, and a request for the WRONG
// contract's paper is visible as an empty answer rather than silently serving
// the same file.
function stub(
  paperByContract: Record<string, unknown[]>,
  contracts: unknown[] = [CONTRACT],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      // The generated client calls fetch with a Request, whose String() is
      // "[object Request]" — reading `.url` is what actually names the path.
      const url = input instanceof Request ? input.url : String(input);
      let body: unknown = { data: contracts };
      if (url.includes("/me")) {
        body = GRANTED;
      } else if (url.includes("/documents")) {
        const asked = new URL(url, "http://x").searchParams.get("contract_id");
        body = { data: paperByContract[asked ?? ""] ?? [] };
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

// The same routing, for a documents endpoint that PAGINATES: each answer is
// keyed on the `cursor` the client sent back, so a row that ignored
// `next_cursor` never reaches the second page and the assertion fails for the
// right reason.
function stubPages(pages: { docs: unknown[]; next: string | null }[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(
        input instanceof Request ? input.url : String(input),
        "http://x",
      );
      if (url.pathname.includes("/me")) {
        return new Response(JSON.stringify(GRANTED), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (!url.pathname.includes("/documents")) {
        return new Response(JSON.stringify({ data: [CONTRACT] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      const cursor = url.searchParams.get("cursor");
      const page = pages[cursor === null ? 0 : Number(cursor)];
      if (!page) {
        throw new Error(`the row walked past the last page (cursor ${cursor})`);
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

describe("a contract's signed paper", () => {
  it("offers the PDF filed against that agreement, named by its file", async () => {
    stub({ "c-1": [PAPER] });
    show(<CompanyContractsCard orgId="o-1" />);

    // The FILENAME is the link text. This row is the only place the paper is
    // read — the library below leaves agreement paper to its agreement — so a
    // generic word for "paper" would leave two files on one agreement as two
    // identical links.
    const link = await screen.findByRole("link", { name: "GR-2026-0092.pdf" });
    expect(link.getAttribute("href")).toBe("/v1/attachments/a-1");
    // The saved file keeps the name it was uploaded under, not the display
    // title the agreement carries.
    expect(link.getAttribute("download")).toBe("GR-2026-0092.pdf");
  });

  it("tells two files on one agreement apart", async () => {
    stub({
      "c-1": [
        PAPER,
        { ...PAPER, id: "a-2", filename: "GR-2026-0092-annex-a.pdf" },
      ],
    });
    show(<CompanyContractsCard orgId="o-1" />);

    // The signed original and its annex differ by one word in the filename and
    // by nothing else on the row. A reader who cannot tell them apart before
    // clicking has been handed a coin flip.
    expect(
      await screen.findByRole("link", { name: "GR-2026-0092.pdf" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "GR-2026-0092-annex-a.pdf" }),
    ).toBeTruthy();
  });

  // #1549: the documents endpoint paginates, so the chips on a row are a PAGE.
  // A row that kept the first page and dropped `page.has_more` presented some
  // of an agreement's paper under a label that reads as all of it.
  it("counts the paper the row is not showing", async () => {
    stubPages([
      {
        docs: [PAPER, { ...PAPER, id: "a-2", filename: "annex-a.pdf" }],
        next: "1",
      },
      {
        docs: [
          { ...PAPER, id: "a-3" },
          { ...PAPER, id: "a-4" },
          { ...PAPER, id: "a-5" },
        ],
        next: null,
      },
    ]);
    show(<CompanyContractsCard orgId="o-1" />);

    // The page the row holds is still offered: the notice is about what is
    // missing, not a reason to withhold what is there.
    expect(
      await screen.findByRole("link", { name: "annex-a.pdf" }),
    ).toBeTruthy();
    expect(await screen.findByText("3 more not shown")).toBeTruthy();
  });

  it("claims nothing about a remainder when the page IS the whole list", async () => {
    stubPages([{ docs: [PAPER], next: null }]);
    show(<CompanyContractsCard orgId="o-1" />);

    await screen.findByRole("link", { name: "GR-2026-0092.pdf" });
    // `has_more: false` is the one answer that entitles the row to read as
    // complete, and a spurious notice would send a reader looking for paper
    // that does not exist.
    expect(screen.queryByText(/more not shown/)).toBeNull();
    expect(screen.queryByText("Showing part of the list")).toBeNull();
  });

  it("says nothing at all when an agreement has no paper on file", async () => {
    // TWO agreements, and only the second has paper. The one that does is what
    // makes the negative assertion mean something: a bare "no link is on
    // screen" passes while the paper read is still in flight, so this waits for
    // a paper read to have LANDED and only then counts the links.
    const SLA = { ...CONTRACT, id: "c-2", title: "Support SLA" };
    stub({ "c-2": [{ ...PAPER, id: "a-9", filename: "SLA-2026.pdf" }] }, [
      CONTRACT,
      SLA,
    ]);
    show(<CompanyContractsCard orgId="o-1" />);
    await screen.findByText("Framework agreement 2026");

    // Recording what was agreed and filing the PDF are separate acts: a
    // commercial record entered from an invoice is complete without a file, so
    // an empty word here would report a gap that is not one.
    await screen.findByRole("link", { name: "SLA-2026.pdf" });
    expect(screen.getAllByRole("link")).toHaveLength(1);
  });
});
