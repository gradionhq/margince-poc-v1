/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompanyDocumentsCard } from "./companydocuments";

// What this card is for: finding a document by the name a human gave it, and
// never inferring which version is current from an upload date or a filename.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const DOCS = [
  {
    id: "d-1",
    filename: "Rahmenvertrag.pdf",
    title: "Framework agreement — signed",
    category: "contract",
    doc_state: "final",
    pinned: true,
    created_at: "2026-08-01T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:x",
  },
  {
    id: "d-2",
    filename: "scan_0001.pdf",
    category: "other",
    doc_state: "draft",
    pinned: false,
    created_at: "2026-08-02T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:x",
  },
  {
    id: "d-3",
    filename: "Kuendigung.pdf",
    category: "legal",
    doc_state: "current",
    pinned: false,
    created_at: "2026-08-03T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:x",
  },
];

function stub(data: unknown[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ data, page: { next_cursor: null } }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}

function show(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("the account's document library", () => {
  it("prefers the display title a human gave over the filename that arrived", async () => {
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    // "Framework agreement — signed" is what a reader looks for;
    // "Rahmenvertrag.pdf" is what the scanner produced.
    expect(
      await screen.findByText("Framework agreement — signed"),
    ).toBeTruthy();
    expect(screen.queryByText("Rahmenvertrag.pdf")).toBeNull();
  });

  it("makes every document's name its download", async () => {
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await screen.findByText("Framework agreement — signed");

    // Every listed document is reachable: authorization is the parent record's,
    // decided before the row was ever returned, so a row a reader can see is a
    // file they can open. A listed row that refused on click was the defect
    // this replaced.
    const links = screen.getAllByRole("link");
    expect(links).toHaveLength(DOCS.length);

    // The name is the link, and the saved file keeps its own filename rather
    // than the display title.
    const signed = screen.getByRole("link", {
      name: "Framework agreement — signed",
    });
    expect(signed.getAttribute("href")).toBe("/v1/attachments/d-1");
    expect(signed.getAttribute("download")).toBe("Rahmenvertrag.pdf");
  });

  it("says the account has no documents rather than leaving the section blank", async () => {
    stub([]);
    show(<CompanyDocumentsCard orgId="o-1" />);
    expect(
      await screen.findByText("No documents on this account yet."),
    ).toBeTruthy();
  });

  it("names the lifecycle state each file asserts", async () => {
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await waitFor(() => expect(screen.getByText("Final")).toBeTruthy());
    // Draft and Current sit beside Final rather than being inferred from
    // upload order: the newest upload is very often a draft.
    expect(screen.getByText("Draft")).toBeTruthy();
    expect(screen.getByText("Pinned")).toBeTruthy();
  });
});
