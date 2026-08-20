/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonFilesTab } from "./personfiles";

type Attachment = components["schemas"]["Attachment"];

// Every field a real row carries: `source` and `captured_by` are stamped by
// the server on every captured attachment, so a fixture missing them is a
// payload no reader ever receives.
const CAPTURED = {
  entity_type: "person",
  source: "upload",
  captured_by: "human:u-1",
  created_at: "2026-08-01T09:00:00Z",
} as const;

function attachment(
  row: Pick<Attachment, "id" | "entity_id" | "filename"> & Partial<Attachment>,
): Attachment {
  return { ...CAPTURED, ...row };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The session and the installation read that the upload in this tab's header
// band makes. Answered from fixtures rather than left to the attachment body,
// because /me rejects a payload with no `user` and a malformed session renders
// every control refused for a reason none of these tests is about.
const SESSION = {
  user: { id: "u-1", email: "rep@example.com", name: "Demo Rep" },
  authorization: {
    seat_type: "full",
    objects: { person: { update: true } },
  },
};

const INSTALLATION = {
  name: "Demo",
  timezone: "Europe/Berlin",
  base_currency: "EUR",
  base_currency_locked: false,
  max_upload_bytes: 26_214_400,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/**
 * A stub that answers the tab's OWN reads through `handler` and everything the
 * header's upload asks for from the fixtures above.
 *
 * Only a request the tab itself made reaches `lastRequest`: the scoping tests
 * read it to prove which person was asked about, and a session probe landing
 * there would answer a question nobody asked.
 */
function stubTab(handler: (request: Request) => Promise<Response> | Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const { pathname } = new URL(request.url);
      if (pathname.endsWith("/me")) {
        return json(SESSION);
      }
      if (pathname.endsWith("/installation/settings")) {
        return json(INSTALLATION);
      }
      lastRequest = request;
      return handler(request);
    }),
  );
}

function stub(body: unknown, status = 200) {
  stubTab(() => json(body, status));
}

// A cursor-paginated library: the first page hands back the cursor the second
// one is only reachable with, so a test that renders the second page has
// proven the walk, not just the button.
function stubTwoPages() {
  stubTab((request) => {
    const cursor = new URL(request.url).searchParams.get("cursor");
    return json(
      cursor === "cur-2"
        ? {
            data: [
              attachment({
                id: "f-2",
                entity_id: "p-1",
                filename: "older.pdf",
              }),
            ],
            page: { has_more: false, next_cursor: null },
          }
        : {
            data: [
              attachment({
                id: "f-1",
                entity_id: "p-1",
                filename: "newest.pdf",
              }),
            ],
            page: { has_more: true, next_cursor: "cur-2" },
          },
    );
  });
}

let lastRequest: Request | undefined;

// A response the test holds open: the stub awaits `arrival`, so the test owns
// the moment a page comes back and can wait on the states either side of it
// instead of on a duration.
function heldPage(): Readonly<{ arrival: Promise<void>; deliver: () => void }> {
  let deliver: () => void = () => undefined;
  const arrival = new Promise<void>((resolve) => {
    deliver = resolve;
  });
  return { arrival, deliver };
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

describe("the person's files tab", () => {
  it("draws every file's name as its own download", async () => {
    stub({
      data: [
        attachment({ id: "f-1", entity_id: "p-1", filename: "quote.pdf" }),
        attachment({ id: "f-2", entity_id: "p-1", filename: "signed.pdf" }),
      ],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    const quote = await screen.findByRole("link", { name: "quote.pdf" });
    expect(quote.getAttribute("href")).toBe("/v1/attachments/f-1");
    expect(quote.getAttribute("download")).toBe("quote.pdf");

    const signed = screen.getByRole("link", { name: "signed.pdf" });
    expect(signed.getAttribute("href")).toBe("/v1/attachments/f-2");
    expect(signed.getAttribute("download")).toBe("signed.pdf");
  });

  it("prefers the title a human gave over the filename that arrived", async () => {
    stub({
      data: [
        attachment({
          id: "f-1",
          entity_id: "p-1",
          filename: "scan_0007.pdf",
          title: "Signed offer letter",
        }),
      ],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    const named = await screen.findByRole("link", {
      name: "Signed offer letter",
    });
    // The name shown is the title, but what lands on disk is still the
    // filename the file actually arrived with.
    expect(named.getAttribute("download")).toBe("scan_0007.pdf");
    expect(screen.queryByText("scan_0007.pdf")).toBeNull();
  });

  it("falls back to the filename when nobody gave the file a title", async () => {
    stub({
      data: [
        attachment({ id: "f-1", entity_id: "p-1", filename: "scan_0007.pdf" }),
      ],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    expect(
      await screen.findByRole("link", { name: "scan_0007.pdf" }),
    ).toBeTruthy();
  });

  it("names the category a file was filed under", async () => {
    stub({
      data: [
        attachment({
          id: "f-1",
          entity_id: "p-1",
          filename: "contract.pdf",
          category: "contract",
        }),
      ],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "contract.pdf" });
    expect(screen.getByText("Contract")).toBeTruthy();
  });

  it("names a channel file as a message attachment, not an email one", async () => {
    stub({
      data: [
        attachment({
          id: "f-1",
          entity_id: "p-1",
          filename: "deck.png",
          category: "message_attachment",
        }),
      ],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "deck.png" });
    expect(screen.getByText("Message attachment")).toBeTruthy();
    // The negative is the point. A missing label falls back to the raw key or
    // the neighbouring one, and a photo from a chat reported as mail is the
    // wrong record this category exists to stop.
    expect(screen.queryByText("Email attachment")).toBeNull();
  });

  it("says nothing about a category the file was never filed under", async () => {
    stub({
      data: [attachment({ id: "f-1", entity_id: "p-1", filename: "scan.pdf" })],
      page: { has_more: false },
    });
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "scan.pdf" });
    // No category was set: the row omits the badge rather than printing an
    // "uncategorised" placeholder for a fact the record never asserted.
    expect(screen.queryByText("Contract")).toBeNull();
    expect(screen.queryByText("Other")).toBeNull();
  });

  it("says the contact has no files rather than leaving the tab blank", async () => {
    stub({ data: [], page: { has_more: false } });
    show(<PersonFilesTab personId="p-1" />);

    expect(
      await screen.findByText("No file has been filed against this contact."),
    ).toBeTruthy();
  });

  it("says the read failed, with a way to retry, rather than reading as empty", async () => {
    stub({ error: "boom" }, 500);
    show(<PersonFilesTab personId="p-1" />);

    expect(await screen.findByText("This section did not load.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    expect(
      screen.queryByText("No file has been filed against this contact."),
    ).toBeNull();
  });

  it("draws a cut page's rows and says the list was cut, not the whole of it", async () => {
    stub({
      data: [attachment({ id: "f-1", entity_id: "p-1", filename: "one.pdf" })],
      page: { has_more: true },
    });
    show(<PersonFilesTab personId="p-1" />);

    expect(await screen.findByRole("link", { name: "one.pdf" })).toBeTruthy();
    expect(screen.getByText("Showing part of the list")).toBeTruthy();
  });

  it("reads the older files behind the first page when the reader asks for more", async () => {
    stubTwoPages();
    const user = userEvent.setup();
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "newest.pdf" });
    expect(screen.getByText("Showing part of the list")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByRole("link", { name: "older.pdf" })).toBeTruthy();
    // The second page lengthens the list rather than replacing it: a reader
    // who has scrolled past the first twenty keeps them.
    expect(screen.getByRole("link", { name: "newest.pdf" })).toBeTruthy();
    if (!lastRequest) {
      throw new Error("the tab asked for nothing at all");
    }
    expect(new URL(lastRequest.url).searchParams.get("cursor")).toBe("cur-2");
  });

  it("says nothing about a cut list once the last page has arrived", async () => {
    stubTwoPages();
    const user = userEvent.setup();
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "newest.pdf" });
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("link", { name: "older.pdf" });

    // Silence is the difference between "all of them" and "the first twenty",
    // so the truncation sentence and the button both go once the walk ends.
    expect(screen.queryByText("Showing part of the list")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("keeps the files already read when a later page fails to load", async () => {
    // The second page is held open and released by the test, because the DOM a
    // failed later page leaves behind is the DOM that was already there: rows,
    // the truncation sentence, and a pressable button. Asserting straight after
    // the click would therefore pass on "nothing has happened yet". The button
    // refusing a press is the walk being out; it becoming pressable again is
    // the failure having landed, and only then is the tab worth asking.
    const secondPage = heldPage();
    let page = 0;
    stubTab(async () => {
      page += 1;
      if (page === 1) {
        return json({
          data: [
            attachment({ id: "f-1", entity_id: "p-1", filename: "newest.pdf" }),
          ],
          page: { has_more: true, next_cursor: "cur-2" },
        });
      }
      await secondPage.arrival;
      return json({ title: "Error" }, 500);
    });
    const user = userEvent.setup();
    show(<PersonFilesTab personId="p-1" />);

    await screen.findByRole("link", { name: "newest.pdf" });
    await user.click(screen.getByRole("button", { name: "Load more" }));

    const loadMore = () => screen.getByRole("button", { name: "Load more" });
    await waitFor(() => expect(loadMore().hasAttribute("disabled")).toBe(true));
    secondPage.deliver();
    await waitFor(() =>
      expect(loadMore().hasAttribute("disabled")).toBe(false),
    );

    // The failure belongs to one page, not to the library: the rows stay, and
    // the button is still there to try the same page again.
    expect(screen.getByRole("link", { name: "newest.pdf" })).toBeTruthy();
    expect(screen.queryByText("This section did not load.")).toBeNull();
  });

  it("files an uploaded document against this contact, from this tab", async () => {
    // Both request shapes reach one stub: the generated client hands `fetch` a
    // whole Request, and the multipart upload calls it the plain way.
    const uploads: FormData[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        // The multipart upload is answered FIRST, because it is the one call
        // made with a relative path — `new URL("/v1/attachments")` throws, so
        // parsing before this branch would fail the request under test.
        if (init?.body instanceof FormData) {
          uploads.push(init.body);
          return json({ id: "att-1", filename: "cv.pdf" }, 201);
        }
        const request = input instanceof Request ? input : null;
        const { pathname } = new URL(request ? request.url : String(input));
        if (pathname.endsWith("/me")) {
          return json(SESSION);
        }
        if (pathname.endsWith("/installation/settings")) {
          return json(INSTALLATION);
        }
        return json({ data: [], page: { has_more: false } });
      }),
    );
    const user = userEvent.setup();
    show(<PersonFilesTab personId="p-7" />);

    // The verb the tab shipped without: a reader who wants a CV on a contact
    // had to reach the upload from the account's library and change its parent.
    await user.click(
      await screen.findByRole("button", { name: "Add a document" }),
    );
    await user.upload(
      screen.getByLabelText(/File/),
      new File(["…"], "cv.pdf", { type: "application/pdf" }),
    );
    const submit = screen.getByRole("button", { name: "Upload" });
    await waitFor(() => expect(submit.hasAttribute("disabled")).toBe(false));
    await user.click(submit);

    await waitFor(() => expect(uploads).toHaveLength(1));
    expect(uploads[0].get("entity_type")).toBe("person");
    expect(uploads[0].get("entity_id")).toBe("p-7");
  });

  it("scopes the request to the person whose files these are", async () => {
    stub({ data: [], page: { has_more: false } });
    show(<PersonFilesTab personId="p-42" />);

    await screen.findByText("No file has been filed against this contact.");
    if (!lastRequest) {
      throw new Error("the tab asked for nothing at all");
    }
    const url = new URL(lastRequest.url);
    expect(url.searchParams.get("entity_type")).toBe("person");
    expect(url.searchParams.get("entity_id")).toBe("p-42");
  });
});
