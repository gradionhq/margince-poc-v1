// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
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

const DEAL = {
  id: "deal-1",
  name: "Pallet Handling Programme — Graz",
  organization_id: "o-1",
  status: "open",
};

// /me refuses a payload without `user` (common.tsx:105), so a fixture that
// carried only the authorization block would leave every control refused for a
// reason the test was not testing.
const USER = { id: "u-1", email: "rep@example.com", name: "Demo Rep" };

const FULL_SEAT = {
  user: USER,
  authorization: {
    seat_type: "full",
    objects: { deal: { update: true }, organization: { update: true } },
  },
};

const READ_SEAT = {
  user: USER,
  authorization: {
    seat_type: "read",
    objects: { deal: { update: true }, organization: { update: true } },
  },
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
        return json({ data: [DEAL], page: { next_cursor: null } });
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
        <AddDocumentDialog orgId="o-1" open={open} onClose={onClose} />
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
        orgId="o-1"
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

    await pickOption(
      user,
      await screen.findByRole("combobox", { name: /About/ }),
      "Pallet Handling Programme — Graz",
    );
    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

    await waitFor(() => expect(uploadedForm(calls)).toBeTruthy());
    const sent = uploadedForm(calls);
    expect(sent.get("entity_type")).toBe("deal");
    expect(sent.get("entity_id")).toBe("deal-1");
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
          <AddDocumentDialog orgId="o-1" open onClose={closed} />
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

  it("says the deals could not be loaded rather than silently offering only the company", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = input instanceof Request ? input.url : String(input);
        if (url.includes("/v1/deals")) {
          return new Response(JSON.stringify({ title: "Server error" }), {
            status: 500,
            headers: { "content-type": "application/json" },
          });
        }
        return new Response(JSON.stringify(FULL_SEAT), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }),
    );
    show();

    expect(await screen.findByText(/deals could not be loaded/)).toBeTruthy();
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
