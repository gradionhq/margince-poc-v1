// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "../design-system/atoms";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { AddDocumentDialog } from "./adddocument";

// What this dialog owes the reader: the file lands on the record they chose,
// and when the two requests behind one press disagree, it says which half
// happened. A dialog that reported the partial as a failure would be inviting a
// second copy of a document that is already stored.

afterEach(() => {
  cleanup();
  shared = undefined;
  vi.unstubAllGlobals();
});

const COMPANY = { record: "organization", id: "o-1" } as const;
const CONTACT = { record: "person", id: "p-1" } as const;

const DEAL = {
  id: "deal-1",
  name: "Pallet Handling Programme — Graz",
  organization_id: "o-1",
  status: "open",
};

// A deal on the SECOND page of the account's deals — the one a flat, capped
// Select could never offer, and the reason the picker walks the pages.
const OLDER_DEAL = {
  id: "deal-99",
  name: "Wash cycle retrofit — Graz plant",
  organization_id: "o-1",
  status: "open",
};

// /me refuses a payload without `user` (common.tsx:105), so a fixture that
// carried only the authorization block would leave every control refused for a
// reason the test was not testing.
const USER = { id: "u-1", email: "rep@example.com", name: "Demo Rep" };

const GRANTS = {
  deal: { update: true },
  organization: { update: true },
  person: { update: true },
};

const FULL_SEAT = {
  user: USER,
  authorization: { seat_type: "full", objects: GRANTS },
};

const READ_SEAT = {
  user: USER,
  authorization: { seat_type: "read", objects: GRANTS },
};

type Recorded = { url: string; method: string; body: unknown };

/**
 * A fetch stub that routes by path and records what was sent.
 *
 * `metadataStatus` is the whole point of the partial-failure test: the upload
 * is allowed to succeed while the PATCH behind it does not.
 */
function stubApi(
  me: unknown,
  options: {
    uploadStatus?: number;
    uploadDetail?: string;
    metadataStatus?: number;
    // Rejects instead of answering — a dropped connection, not a refusal.
    metadataThrows?: boolean;
    uploadThrows?: boolean;
    // Resolves the upload only once the test lets it, so the in-flight window
    // is a state the test can act in rather than a race it has to win.
    holdUpload?: Promise<void>;
    // What this installation says it accepts. Absent means the read answers
    // without the field, which is the "server has not said" case.
    maxUploadBytes?: number;
    // Refuses the installation read outright — the other way the client can end
    // up without a limit.
    settingsStatus?: number;
    // Refuses the deal read, so the picker has a failure to report.
    dealsStatus?: number;
  } = {},
) {
  const calls: Recorded[] = [];
  const json = (payload: unknown, status = 200) =>
    new Response(JSON.stringify(payload), {
      status,
      headers: { "content-type": "application/json" },
    });

  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // Two shapes reach this stub, and a test that handled only one would
      // silently answer nothing: the generated client hands `fetch` a whole
      // `Request`, while the hand-rolled multipart upload calls it the plain
      // way, with a url and an init.
      const request = input instanceof Request ? input : null;
      const url = request ? request.url : String(input);
      const method = request ? request.method : (init?.method ?? "GET");
      const body = request ? await request.clone().text() : init?.body;
      calls.push({ url, method, body });

      if (url.includes("/v1/me")) {
        return json(me);
      }
      if (url.includes("/installation/settings")) {
        if (options.settingsStatus) {
          return json(
            { title: "Server error", status: options.settingsStatus },
            options.settingsStatus,
          );
        }
        return json({
          name: "Demo",
          timezone: "Europe/Berlin",
          base_currency: "EUR",
          base_currency_locked: false,
          max_upload_bytes: options.maxUploadBytes,
        });
      }
      if (url.includes("/v1/deals")) {
        if (options.dealsStatus) {
          return json(
            { title: "The deal list is unavailable", status: 500 },
            options.dealsStatus,
          );
        }
        // TWO pages, walked by cursor. The second one holds a deal that no
        // single request offers, which is the whole point of the walk: the flat
        // Select this replaced could not reach it at any page size.
        const cursor = new URL(url).searchParams.get("cursor");
        return cursor === "page-2"
          ? json({ data: [OLDER_DEAL], page: { has_more: false } })
          : json({
              data: [DEAL],
              page: { has_more: true, next_cursor: "page-2" },
            });
      }
      if (url.includes("/metadata")) {
        if (options.metadataThrows) {
          throw new TypeError("Failed to fetch");
        }
        const status = options.metadataStatus ?? 200;
        return status === 200
          ? json({ id: "att-1" })
          : json({ title: "Forbidden", status }, status);
      }
      if (url.includes("/v1/attachments")) {
        if (options.uploadThrows) {
          throw new TypeError("Failed to fetch");
        }
        if (options.holdUpload) {
          await options.holdUpload;
        }
        const status = options.uploadStatus ?? 201;
        return status === 201
          ? json({ id: "att-1", filename: "order_form.txt" }, 201)
          : json(
              {
                title: "Unprocessable",
                status,
                detail: options.uploadDetail,
              },
              status,
            );
      }
      return json({ data: [], page: { next_cursor: null } });
    }),
  );
  return calls;
}

const client = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false } } });

let shared: QueryClient | undefined;

/** The dialog under one stable client, so a rerender is a reopen and not a
 * fresh mount — which is the only way the state-between-opens rule can be
 * tested at all. */
function dialog(open: boolean, onClose = () => {}) {
  shared ??= client();
  return (
    <QueryClientProvider client={shared}>
      <LocaleProvider initial="en">
        <AddDocumentDialog anchor={COMPANY} open={open} onClose={onClose} />
      </LocaleProvider>
    </QueryClientProvider>
  );
}

function renderDialog(open: boolean, onClose = () => {}) {
  shared = client();
  return render(dialog(open, onClose));
}

/**
 * The dialog as its real caller holds it: a parent owning the open flag, which
 * the dialog's own Cancel closes through `onClose`.
 *
 * Rerendering with `open={false}` by hand would not do — that skips the
 * dialog's close path entirely, and the close path is what the test is about.
 */
function Hosted() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <Button onClick={() => setOpen(true)}>reopen</Button>
      <AddDocumentDialog
        anchor={COMPANY}
        open={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
}

function renderHosted() {
  shared = client();
  return render(
    <QueryClientProvider client={shared}>
      <LocaleProvider initial="en">
        <Hosted />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

function show(onClose = () => {}) {
  return renderDialog(true, onClose);
}

function orderForm() {
  return new File(["EUR 148,500.00"], "order_form.txt", {
    type: "text/plain",
  });
}

/** A file of exactly `size` bytes, for the cases that are about size alone. */
function fileOf(size: number) {
  return new File(["x".repeat(size)], "scan.pdf", { type: "application/pdf" });
}

/** Every multipart upload the dialog sent. Its LENGTH is the duplicate check. */
function uploads(calls: Recorded[]): Recorded[] {
  return calls.filter(
    (call) => call.method === "POST" && call.url.includes("/v1/attachments"),
  );
}

/** The multipart body the dialog sent, as plain fields. */
function uploadedForm(calls: Recorded[]): FormData {
  const upload = uploads(calls)[0];
  if (!(upload?.body instanceof FormData)) {
    throw new Error("no multipart upload was sent");
  }
  return upload.body;
}

/**
 * Press Upload once it is actually offered.
 *
 * The button is refused until /me has answered, so a click sent the moment the
 * file is chosen lands on a disabled control and does nothing — a test that
 * skipped this wait would assert against an upload that was never attempted.
 */
/**
 * Choose "A deal", search for one by name, and pick it.
 *
 * The candidate is awaited rather than assumed: RecordPicker debounces the
 * typed term, and every page the walk reads is a request — so the button only
 * exists once the walk that found it has come back.
 */
async function pickDeal(user: UserEvent, term: string, name: RegExp) {
  await user.click(screen.getByRole("radio", { name: /A deal/ }));
  await user.type(
    screen.getByRole("searchbox", { name: /Search this account/ }),
    term,
  );
  await user.click(await screen.findByRole("button", { name }));
}

async function pressUpload(user: UserEvent) {
  const submit = screen.getByRole("button", { name: "Upload" });
  await waitFor(() => expect(submit.hasAttribute("disabled")).toBe(false));
  await user.click(submit);
}

describe("adding a document from the account", () => {
  it("files the document against the company by default", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    const sent = uploadedForm(calls);
    expect(sent.get("entity_type")).toBe("organization");
    expect(sent.get("entity_id")).toBe("o-1");
    expect((sent.get("file") as File).name).toBe("order_form.txt");
  });

  it("files it against the deal the reader picked, which is the only kind that can be read for deal fields", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    await pickDeal(user, "pallet", /Pallet Handling Programme/);
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    const sent = uploadedForm(calls);
    expect(sent.get("entity_type")).toBe("deal");
    expect(sent.get("entity_id")).toBe("deal-1");
  });

  it("reaches a deal on a later page, which no single request offers", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    // `/deals` carries no text query, so the words are matched over pages this
    // dialog walks. A deal past the first page is exactly the one the capped
    // Select could not offer at any size.
    await pickDeal(user, "wash", /Wash cycle retrofit/);
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    expect(uploadedForm(calls).get("entity_id")).toBe("deal-99");
    // The walk continued with the cursor the first page handed back, rather
    // than asking the same question twice with a larger limit.
    const walked = calls.filter((call) => call.url.includes("/v1/deals"));
    expect(
      walked.some(
        (call) => new URL(call.url).searchParams.get("cursor") === "page-2",
      ),
    ).toBe(true);
  });

  it("says how far the deal search reaches, before the reader fails to find something", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT);
    show();

    await user.click(screen.getByRole("radio", { name: /A deal/ }));

    // An unfound deal and a deal that does not exist read identically, so the
    // control states its own reach rather than leaving the reader to conclude
    // the account has no such deal.
    expect(
      screen.getByText(/covers this account's 2000 newest deals/),
    ).toBeTruthy();
  });

  it("refuses the upload while the chosen filing has no deal, rather than falling back to the company", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    await user.click(screen.getByRole("radio", { name: /A deal/ }));
    await user.upload(screen.getByLabelText(/File/), orderForm());

    // Filing it against the company instead would put the document somewhere
    // the reader did not choose — and only a deal's documents can be read for
    // deal fields, so the fallback is a silent downgrade.
    const submit = screen.getByRole("button", { name: "Upload" });
    await waitFor(() => expect(submit.hasAttribute("disabled")).toBe(true));
    expect(
      screen.getByText("Pick the deal to file this against."),
    ).toBeTruthy();
    await user.click(submit);
    expect(uploads(calls)).toHaveLength(0);
  });

  it("reports a failed deal search in the picker rather than offering nothing", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, { dealsStatus: 500 });
    show();

    await user.click(screen.getByRole("radio", { name: /A deal/ }));
    await user.type(
      screen.getByRole("searchbox", { name: /Search this account/ }),
      "graz",
    );

    // An empty candidate list would say "this account has no such deal", which
    // is a claim about the account made from a request that failed.
    expect(
      await screen.findByText("The deal list is unavailable"),
    ).toBeTruthy();
  });

  it("asks nothing about deals when the dialog was opened from a contact", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    shared = client();
    render(
      <QueryClientProvider client={shared}>
        <LocaleProvider initial="en">
          <AddDocumentDialog anchor={CONTACT} open onClose={() => {}} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    // A deal hangs off a company and nothing on a contact's page names one, so
    // the question has no second answer — and a control whose one option is the
    // record you are already on is a control that asks nothing.
    expect(screen.queryByRole("radio", { name: /A deal/ })).toBeNull();
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    const sent = uploadedForm(calls);
    expect(sent.get("entity_type")).toBe("person");
    expect(sent.get("entity_id")).toBe("p-1");
    // Nothing asked the account's deals about a contact's file.
    expect(calls.some((call) => call.url.includes("/v1/deals"))).toBe(false);
  });

  it("sends the category and title as a second request, because the upload cannot carry them", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    const closed = vi.fn();
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider initial="en">
          <AddDocumentDialog anchor={COMPANY} open onClose={closed} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    await pickOption(
      user,
      screen.getByRole("combobox", { name: /Category/ }),
      "Contract",
    );
    await user.type(screen.getByLabelText(/Title/), "Signed order form");
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(closed).toHaveBeenCalled());
    const patch = calls.find((call) => call.url.includes("/metadata"));
    expect(patch?.method).toBe("PATCH");
    expect(JSON.parse(String(patch?.body))).toEqual({
      category: "contract",
      title: "Signed order form",
    });
  });

  it("does not offer a provenance category a hand upload cannot honestly claim", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT);
    show();

    await user.click(screen.getByRole("combobox", { name: /Category/ }));
    const listbox = screen.getByRole("listbox");
    // What a human uploading from their own disk MAY say about a file.
    for (const offered of ["Contract", "Offer", "Legal", "Other"]) {
      expect(
        within(listbox).getByRole("option", { name: offered }),
      ).toBeTruthy();
    }
    // And what they may not. Both `*_attachment` values record which transport
    // carried a file, which capture derives; a file arriving through this dialog
    // arrived on none, so choosing one would mint a false answer to the exact
    // question the document library reads that column for.
    expect(
      within(listbox).queryByRole("option", { name: "Email attachment" }),
    ).toBeNull();
    expect(
      within(listbox).queryByRole("option", { name: "Message attachment" }),
    ).toBeNull();
  });

  it("does not send a metadata request when the reader changed neither field", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    // Writing the defaults back would overwrite whatever the server derived.
    expect(calls.some((call) => call.url.includes("/metadata"))).toBe(false);
  });

  it("says the file is stored when only the metadata failed, and clears it so nobody uploads twice", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, { metadataStatus: 403 });
    const closed = vi.fn();
    show(closed);

    await pickOption(
      user,
      screen.getByRole("combobox", { name: /Category/ }),
      "Contract",
    );
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    expect(await screen.findByText(/Uploaded, but not filed/)).toBeTruthy();
    // The dialog stays open — but the file it holds is gone, because that file
    // is already on the record.
    expect(closed).not.toHaveBeenCalled();
    expect(
      screen.getByText("Drop the file here, or click to choose one"),
    ).toBeTruthy();
  });

  it("reports an upload that never landed as nothing stored, which is a different sentence", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, { uploadThrows: true });
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    // A connection that dropped before the POST carries no problem document,
    // so this is the one case the dialog's own sentence is the best available.
    // It must not be confused with the partial, where the bytes DID land.
    expect(await screen.findByText(/Nothing was stored/)).toBeTruthy();
    expect(screen.queryByText(/Uploaded, but not filed/)).toBeNull();
  });

  it("refuses before a file is chosen, and says what is missing", async () => {
    stubApi(FULL_SEAT);
    show();

    // Awaited, because until /me answers the refusal on offer is the seat one:
    // asserting immediately would pass on the wrong reason.
    expect(await screen.findByText("Choose a file to upload.")).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Upload" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  it("refuses a read-only seat even though its RBAC grant says update", async () => {
    const user = userEvent.setup();
    const calls = stubApi(READ_SEAT);
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await waitFor(() =>
      expect(
        screen.getByText("You may not add documents to this record."),
      ).toBeTruthy(),
    );
    // The seat is clamped by the server on the METHOD, before RBAC — a grant
    // alone is not permission to write, and the button must not pretend it is.
    // Pressed anyway, because a disabled attribute a test never exercises is a
    // claim about markup rather than about behaviour.
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(calls.some((call) => call.method === "POST")).toBe(false);
  });

  it("refuses a second press while the first upload is still in flight", async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const calls = stubApi(FULL_SEAT, { holdUpload: held });
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    // The refusal has to be on the CONTROL, not only in its label: a button
    // that merely reads "Uploading…" still fires, and the second press puts a
    // second copy of the document on an audited record.
    const submit = await screen.findByRole("button", { name: /Uploading/ });
    expect(submit.hasAttribute("disabled")).toBe(true);
    await user.click(submit);

    release?.();
    await waitFor(() => expect(uploads(calls)).toHaveLength(1));
  });

  it("starts empty on the next opening, so the file just filed cannot be filed again", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    const view = renderDialog(true);

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);
    await waitFor(() => expect(uploads(calls)).toHaveLength(1));

    // The dialog is never unmounted — Modal renders null while shut — so a
    // reopen shows whatever the last visit left behind.
    view.rerender(dialog(false));
    view.rerender(dialog(true));

    expect(
      await screen.findByText("Drop the file here, or click to choose one"),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Upload" })).toBeTruthy();
    expect(await screen.findByText("Choose a file to upload.")).toBeTruthy();
  });

  it("does not greet the next visit with a warning about the last one", async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    stubApi(FULL_SEAT, { holdUpload: held, metadataThrows: true });
    renderHosted();

    await pickOption(
      user,
      screen.getByRole("combobox", { name: /Category/ }),
      "Contract",
    );
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    // Closed while the request is still out. React Query runs a mutation to
    // completion whoever started it, so the half-failure lands with nobody
    // watching.
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    release?.();
    await waitFor(() =>
      expect(screen.queryByText("Add a document")).toBeNull(),
    );

    await user.click(screen.getByRole("button", { name: "reopen" }));
    expect(
      await screen.findByText("Drop the file here, or click to choose one"),
    ).toBeTruthy();
    expect(screen.queryByText(/Uploaded, but not filed/)).toBeNull();
  });

  it("calls a thrown metadata request a partial success, because the bytes are already stored", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, { metadataThrows: true });
    show();

    await pickOption(
      user,
      screen.getByRole("combobox", { name: /Category/ }),
      "Contract",
    );
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    // A dropped connection after a successful POST rejects rather than
    // returning a problem document. Reported as a failure it would read
    // "Nothing was stored" over a document that is.
    expect(await screen.findByText(/Uploaded, but not filed/)).toBeTruthy();
    expect(screen.queryByText(/Nothing was stored/)).toBeNull();
  });

  it("says what the server said when it refused, rather than one fixed sentence", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, {
      uploadStatus: 422,
      uploadDetail: "the file exceeds the 26214400-byte limit",
    });
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    // "Try again, or choose another file" is wrong advice for an oversize file
    // and for a denial alike; the server's own detail is the actionable half.
    expect(
      await screen.findByText(/exceeds the 26214400-byte limit/),
    ).toBeTruthy();
  });
  // What this installation accepts is the OPERATOR's number, so the form has to
  // ask rather than compile one in. Getting it wrong costs either a wasted
  // upload of a file that was never going to be taken, or a refusal of one that
  // would have been.
  it("states the limit this installation actually enforces", async () => {
    stubApi(FULL_SEAT, { maxUploadBytes: 3_000_000 });
    show();

    expect(await screen.findByText("Up to 3 MB.")).toBeTruthy();
  });

  it("refuses an oversize file without sending it", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT, { maxUploadBytes: 3_000_000 });
    show();

    await screen.findByText("Up to 3 MB.");
    await user.upload(screen.getByLabelText(/File/), fileOf(3_000_001));

    const submit = screen.getByRole("button", { name: "Upload" });
    await waitFor(() => expect(submit.hasAttribute("disabled")).toBe(true));
    await user.click(submit);

    // The refusal names the limit, and NOTHING went over the wire: refusing
    // after a 3 MB round trip is the cost this check exists to avoid.
    expect(screen.getByText(/larger than 3 MB/)).toBeTruthy();
    expect(uploads(calls)).toHaveLength(0);
  });

  it("sends a file exactly at the limit rather than refusing it here", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT, { maxUploadBytes: 3_000_000 });
    show();

    await screen.findByText("Up to 3 MB.");
    await user.upload(screen.getByLabelText(/File/), fileOf(3_000_000));
    await pressUpload(user);

    // What this proves is that the CLIENT does not refuse it — not that the
    // server takes it. The ceiling bounds the whole request, so a file within a
    // few hundred bytes of the limit is still refused by the server once part
    // framing is counted, and that refusal names the same number.
    //
    // Erring this way on purpose. Subtracting a margin here would refuse files
    // the installation would have accepted, over a number the reader was never
    // shown; letting the last fraction of a percent through costs one wasted
    // request and produces an honest message from the side that decides.
    await waitFor(() => expect(uploads(calls)).toHaveLength(1));
  });

  it("leaves the refusal to the server until the installation has answered", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT);
    show();

    await user.upload(screen.getByLabelText(/File/), fileOf(50_000_000));
    await pressUpload(user);

    // No answer means no local limit — not a guessed one. Guessing would refuse
    // a file the installation may well accept, and the reader has no way to
    // argue with a number the client invented.
    await waitFor(() => expect(uploads(calls)).toHaveLength(1));
    expect(screen.queryByText(/larger than/)).toBeNull();
  });
  it("still uploads when the installation read fails outright", async () => {
    const user = userEvent.setup();
    const calls = stubApi(FULL_SEAT, { settingsStatus: 500 });
    show();

    await user.upload(screen.getByLabelText(/File/), fileOf(50_000_000));
    await pressUpload(user);

    // A failed settings read is not this dialog's problem to report: the screen
    // it belongs to says so, and the upload still has a server that will refuse
    // it if it must. What must NOT happen is a banner here, or a refusal over a
    // limit nobody ever stated.
    await waitFor(() => expect(uploads(calls)).toHaveLength(1));
    expect(screen.queryByText(/larger than/)).toBeNull();
    expect(screen.queryByText(/Up to/)).toBeNull();
  });
});
