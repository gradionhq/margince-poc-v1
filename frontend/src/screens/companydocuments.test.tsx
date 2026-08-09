/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { CompanyDocumentsCard } from "./companydocuments";

// A document library that shows a download button which fails on click teaches
// a reader to distrust the ones that work. That, and never inferring which
// version is current, are what this card is for.

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
    scan_status: "clean",
    created_at: "2026-08-01T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    workspace_id: "w-1",
    source: "upload",
    captured_by: "human:x",
  },
  {
    id: "d-2",
    filename: "scan_0001.pdf",
    category: "other",
    doc_state: "draft",
    pinned: false,
    scan_status: "scanning",
    created_at: "2026-08-02T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    workspace_id: "w-1",
    source: "upload",
    captured_by: "human:x",
  },
  {
    id: "d-3",
    filename: "blocked.pdf",
    category: "other",
    doc_state: "current",
    pinned: false,
    scan_status: "blocked",
    created_at: "2026-08-03T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    workspace_id: "w-1",
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

  it("offers no download for a file whose bytes cannot be served", async () => {
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await screen.findByText("Framework agreement — signed");

    // Both are LISTED — hiding them would claim they do not exist — but the
    // scan gates the byte stream, so neither gets a link that would 409.
    expect(screen.getByText(/Scanning/)).toBeTruthy();
    expect(screen.getByText(/Blocked by the scanner/)).toBeTruthy();
    // One clean file, so exactly one download link.
    expect(screen.getAllByRole("link", { name: "Download" })).toHaveLength(1);
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
