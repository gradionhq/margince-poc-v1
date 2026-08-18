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
