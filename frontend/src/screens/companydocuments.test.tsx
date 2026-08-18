/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
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
    // "Framework agreement — signed" is what a reader looks for and what the
    // row is named by; "Rahmenvertrag.pdf" is what the scanner produced, and it
    // is what lands in the downloads folder — so it reads underneath as the
    // quieter of the two rather than in the name's place.
    const named = await screen.findByRole("link", {
      name: "Framework agreement — signed",
    });
    expect(named.getAttribute("download")).toBe("Rahmenvertrag.pdf");
    expect(
      screen.getByText("Rahmenvertrag.pdf").closest(".rec-meta"),
    ).toBeTruthy();
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

  it("leaves a document filed against an agreement to that agreement", async () => {
    // The contracts card above renders this file on the row for the agreement
    // it covers. Listing it here as well made one signed PDF read as two
    // documents on a single tab.
    stub([
      ...DOCS,
      {
        ...DOCS[0],
        id: "d-4",
        title: "Framework agreement 2026",
        filename: "GR-2026-0092.pdf",
        contract_id: "c-1",
      },
    ]);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await screen.findByText("Framework agreement — signed");
    expect(screen.queryByText("Framework agreement 2026")).toBeNull();
  });

  it("says why the library is empty when every file belongs to an agreement", async () => {
    stub([{ ...DOCS[0], contract_id: "c-1" }]);
    show(<CompanyDocumentsCard orgId="o-1" />);
    // "No documents on this account yet" would be a lie about an account that
    // has one: the file is upstairs, and the copy has to say so.
    expect(
      await screen.findByText(
        "Every document here is filed against an agreement above.",
      ),
    ).toBeTruthy();
  });

  it("keeps replaced versions out of the list until they are asked for", async () => {
    stub([
      ...DOCS,
      {
        ...DOCS[1],
        id: "d-5",
        filename: "scan_0001_v0.pdf",
        doc_state: "superseded",
      },
    ]);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await screen.findByText("Framework agreement — signed");
    // Three uploads of one document are one document to a rep. The history is
    // reachable, not gone: the footer says how much of it is being held back,
    // beside the control that holds it.
    expect(screen.queryByText("scan_0001_v0.pdf")).toBeNull();
    expect(screen.getByText("1 superseded document is hidden.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Show superseded" }));
    expect(await screen.findByText("scan_0001_v0.pdf")).toBeTruthy();
  });

  it("opens a document's staged reading only when a reader asks for it", async () => {
    stub([{ ...DOCS[1], entity_type: "deal", entity_id: "dl-1" }]);
    show(<CompanyDocumentsCard orgId="o-1" />);
    // Each reading panel asks the server for its own document on mount, so a
    // list that opened them all fired one request per deal file and buried the
    // filenames the reader came for.
    const toggle = await screen.findByRole("button", {
      name: "Read this document",
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(toggle);
    expect(
      await screen.findByRole("button", { name: "Hide the reading" }),
    ).toBeTruthy();
  });

  it("names the lifecycle state each file asserts", async () => {
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await waitFor(() => expect(screen.getByText("Final")).toBeTruthy());
    // Draft and Current sit beside Final rather than being inferred from
    // upload order: the newest upload is very often a draft.
    expect(screen.getByText("Draft")).toBeTruthy();
    expect(screen.getByText("Current")).toBeTruthy();
  });

  it("says nothing about pinning, which nothing in this product can do", async () => {
    // `pinned` is on the wire and the endpoint sorts on it, but no surface sets
    // it — so a badge here could only mark a document pinned through the API by
    // hand, and a state a reader can see and cannot reach reads as broken
    // rather than absent.
    stub(DOCS);
    show(<CompanyDocumentsCard orgId="o-1" />);
    await screen.findByText("Framework agreement — signed");
    expect(screen.queryByText("Pinned")).toBeNull();
  });
});
