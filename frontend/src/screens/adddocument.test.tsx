/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { AddDocumentDialog } from "./adddocument";

// What this dialog owes the reader: the file lands on the record they chose,
// and when the two requests behind one press disagree, it says which half
// happened. A dialog that reported the partial as a failure would be inviting a
// second copy of a document that is already stored.

afterEach(() => {
  cleanup();
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
  options: { uploadStatus?: number; metadataStatus?: number } = {},
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
      if (url.includes("/v1/deals")) {
        return json({ data: [DEAL], page: { next_cursor: null } });
      }
      if (url.includes("/metadata")) {
        const status = options.metadataStatus ?? 200;
        return status === 200
          ? json({ id: "att-1" })
          : json({ title: "Forbidden", status }, status);
      }
      if (url.includes("/v1/attachments")) {
        const status = options.uploadStatus ?? 201;
        return status === 201
          ? json({ id: "att-1", filename: "order_form.txt" }, 201)
          : json({ title: "Forbidden", status }, status);
      }
      return json({ data: [], page: { next_cursor: null } });
    }),
  );
  return calls;
}

function show(onClose = () => {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <AddDocumentDialog orgId="o-1" open onClose={onClose} />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

function orderForm() {
  return new File(["EUR 148,500.00"], "order_form.txt", {
    type: "text/plain",
  });
}

/** The multipart body the dialog sent, as plain fields. */
function uploadedForm(calls: Recorded[]): FormData {
  const upload = calls.find(
    (call) => call.method === "POST" && call.url.includes("/v1/attachments"),
  );
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

  it("reports a failed upload as nothing stored, which is a different sentence", async () => {
    const user = userEvent.setup();
    stubApi(FULL_SEAT, { uploadStatus: 403 });
    show();

    await user.upload(screen.getByLabelText(/File/), orderForm());
    await pressUpload(user);

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
    expect(
      screen.getByRole("button", { name: "Upload" }).hasAttribute("disabled"),
    ).toBe(true);
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
});
