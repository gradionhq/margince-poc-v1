/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DocumentExtractionPanel } from "./documentextraction";

// What this panel must never do is collapse the three answers a reading can
// give. A rep who cannot tell "still reading" from "read it and it says none of
// them" from "could not read it" will either distrust a correct empty answer or
// trust a broken one — so each state gets its own test, asserting the words that
// separate it from its neighbours rather than merely that something rendered.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Extraction = components["schemas"]["AttachmentExtraction"];

const GROUNDED: Extraction = {
  id: "11111111-1111-1111-1111-111111111111",
  status: "done",
  created_at: "2026-08-15T09:00:00Z",
  fields: [
    {
      field: "amount_minor",
      value: "14850000",
      source_quote: "Contract value: EUR 148,500.00",
      page_or_section: "2. Commercial terms",
      confidence: "high",
    },
    {
      field: "currency",
      value: "EUR",
      source_quote: "Contract value: EUR 148,500.00",
      page_or_section: "2. Commercial terms",
      confidence: "medium",
    },
  ],
  omitted: [{ field: "expected_close_date", reason: "not_stated_in_file" }],
};

// serve answers the poll with one body, and records every request so a test can
// assert what the panel SENT — which is where the reading-id guarantee lives.
function serve(body: unknown, status = 200) {
  const calls: { url: string; method: string; body: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    // openapi-fetch hands fetch a Request rather than (url, init) for a body
    // call, so reading the method off `init` alone would record every POST as a
    // GET — and the assertions about what was SENT would pass against a panel
    // that sent nothing.
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = request ? request.url : String(input);
      const method = request?.method ?? init?.method ?? "GET";
      let sent: unknown;
      if (request && method !== "GET") {
        sent = JSON.parse(await request.clone().text());
      } else if (init?.body) {
        sent = JSON.parse(String(init.body));
      }
      calls.push({ url, method, body: sent });
      if (method !== "GET") {
        return new Response(JSON.stringify({}), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response(status === 404 ? "" : JSON.stringify(body), {
        status,
        headers: { "content-type": "application/json" },
      });
    }),
  );
  return calls;
}

function show(canAccept = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <DocumentExtractionPanel attachmentId="att-1" canAccept={canAccept} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("the three answers a reading can give", () => {
  it("offers to read a document nothing has read", async () => {
    serve(null, 404);
    show();
    expect(
      await screen.findByRole("button", { name: /read this file/i }),
    ).toBeTruthy();
  });

  it("says it is still reading rather than showing an empty result", async () => {
    serve({ ...GROUNDED, status: "running", fields: [], omitted: [] });
    show();
    expect(await screen.findByText(/reading this file/i)).toBeTruthy();
    // The distinction under test: a running reading must not be describable as
    // a document that states nothing.
    expect(screen.queryByText(/states none of the deal fields/i)).toBeNull();
  });

  it("reports a document that states none of them as an ANSWER, with its reason", async () => {
    serve({
      ...GROUNDED,
      fields: [],
      omitted: [{ field: "amount_minor", reason: "not_stated_in_file" }],
      status_detail:
        "this document states none of the deal fields clearly enough to offer one",
    });
    show();
    expect(await screen.findByText(/^AI read this file and it/i)).toBeTruthy();
    // The reading's own words, alongside the panel's: an empty result that does
    // not explain itself reads as a broken feature.
    expect(screen.getByText(/clearly enough to offer one/i)).toBeTruthy();
    // Not a failure, and it must not offer the retry that a failure offers.
    expect(screen.queryByText(/could not be read/i)).toBeNull();
  });

  it("reports a failed reading with the reason and a way to try again", async () => {
    serve({
      ...GROUNDED,
      status: "failed",
      fields: [],
      omitted: [],
      status_detail:
        "this installation's model cannot read a image/tiff document",
    });
    show();
    expect(await screen.findByText(/could not be read/i)).toBeTruthy();
    // The reason is the product: "it failed" tells a rep nothing to act on.
    expect(screen.getByText(/cannot read a image\/tiff/i)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /try reading it again/i }),
    ).toBeTruthy();
  });
});

describe("what a grounded reading offers", () => {
  it("names the count, shows each value, and says what was omitted and why", async () => {
    serve(GROUNDED);
    show();
    expect(await screen.findByText(/2 fields it can ground/i)).toBeTruthy();
    // MONEY, not the minor units it is stored in. "14850000" under a label
    // reading "Amount", beside a snippet quoting "EUR 148,500.00", is two
    // renderings of one number disagreeing a hundredfold — in the one place a
    // human is asked to check it.
    expect(screen.getByText(/148,500\.00/)).toBeTruthy();
    expect(screen.queryByText("14850000")).toBeNull();
    expect(screen.getByText("EUR")).toBeTruthy();
    // An omission is an answer, rendered rather than left off for the reader to
    // notice on their own.
    expect(screen.getByText(/not stated in this file/i)).toBeTruthy();
  });

  it("tells the two omission reasons apart", async () => {
    serve({
      ...GROUNDED,
      omitted: [
        { field: "expected_close_date", reason: "not_stated_in_file" },
        { field: "name", reason: "not_confidently_stated" },
      ],
    });
    show();
    expect(await screen.findByText(/not stated in this file/i)).toBeTruthy();
    // The floor has to stay visible: folding this into "not stated" would make
    // every under-confident reading look like a silent document.
    expect(screen.getByText(/not clearly enough to accept/i)).toBeTruthy();
  });

  it("sends the id of the reading it is SHOWING when accepting", async () => {
    const calls = serve(GROUNDED);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: /accept 2 fields/i }),
    );
    await waitFor(() => {
      const accept = calls.find((c) => c.method === "POST");
      if (!accept) {
        throw new Error("the accept never reached the wire");
      }
      // The whole point of the id: what lands on the deal is what was on screen,
      // not whatever reading happens to be newest when the click arrives.
      expect((accept.body as { extraction_id: string }).extraction_id).toBe(
        GROUNDED.id,
      );
    });
  });

  it("flips to the accepted state naming the count, with the controls gone", async () => {
    serve(GROUNDED);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: /accept 2 fields/i }),
    );
    expect(
      await screen.findByText(/2 fields accepted to the deal/i),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /accept/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /dismiss/i })).toBeNull();
  });

  it("writes nothing on dismiss and says so", async () => {
    const calls = serve(GROUNDED);
    show();
    await userEvent.click(
      await screen.findByRole("button", { name: /dismiss/i }),
    );
    expect(await screen.findByText(/nothing was written/i)).toBeTruthy();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  // The edit path is where the minor-unit display would have done real damage:
  // a rep "correcting" 14850000 to the figure the document prints turns €148,500
  // into €1,485. So the field is edited in MAJOR units and converted back here.
  it("takes an edit in major units and sends it as minor units", async () => {
    const calls = serve(GROUNDED);
    show();
    const [editAmount] = await screen.findAllByRole("button", {
      name: /^edit$/i,
    });
    await userEvent.click(editAmount);
    const input = screen.getByRole("textbox", { name: /edit amount/i });
    // It opens on the figure a person says, not the integer we store.
    expect((input as HTMLInputElement).value).toBe("148500.00");
    await userEvent.clear(input);
    await userEvent.type(input, "200000");
    await userEvent.click(
      screen.getByRole("button", { name: /accept 2 fields/i }),
    );
    await waitFor(() => {
      const accept = calls.find((c) => c.method === "POST");
      if (!accept) {
        throw new Error("the accept never reached the wire");
      }
      const body = accept.body as { edits: Record<string, string> };
      expect(body.edits).toEqual({ amount_minor: "20000000" });
    });
  });

  it("offers no accept to a reader who may not write the deal", async () => {
    serve(GROUNDED);
    show(false);
    expect(await screen.findByText(/2 fields it can ground/i)).toBeTruthy();
    // The values and their evidence still render: seeing what a document says is
    // not the same authority as writing it onto a record.
    expect(screen.getByText(/148,500\.00/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /accept/i })).toBeNull();
  });
});
