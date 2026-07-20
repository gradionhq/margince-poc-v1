/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ComposeModal, RelinkModal, TimelineActions } from "./compose";

type Activity = components["schemas"]["Activity"];

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// A 501 answer carries no JSON body (the mailer/model is simply not wired), so
// the composer must branch on the raw status, not on a parsed problem.
function emptyResponse(status: number) {
  return new Response(null, { status });
}

function problemResponse(body: unknown, status: number) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const PURPOSES = {
  data: [
    {
      id: "p1",
      workspace_id: "w",
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// Records every request so a test can assert what actually went to the server
// — the request body and headers ARE the contract for a send/relink.
type Sent = { key: string; body: unknown; headers: Headers };

function stubRoutes(overrides: Record<string, () => Response> = {}) {
  const sent: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.clone().json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      const headers = request
        ? request.headers
        : new Headers(init?.headers ?? {});
      sent.push({ key, body, headers });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /consent-purposes") return jsonResponse(PURPOSES);
      return jsonResponse({});
    }),
  );
  return sent;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const activity202: Activity = {
  id: "act-1",
  workspace_id: "w",
  kind: "email",
  subject: "Re: Q3",
  occurred_at: "2026-07-01T00:00:00Z",
  is_done: false,
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
};

describe("RelinkModal", () => {
  it("relinks the search-picked target and closes on 200", async () => {
    const onClose = vi.fn();
    const sent = stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [{ type: "deal", id: "d-9", title: "Acme renewal" }],
          page: { has_more: false },
        }),
      "POST /activities/act-1/relink": () => jsonResponse(activity202),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={onClose}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "Acme");
    const candidate = await screen.findByRole("button", {
      name: "Acme renewal",
    });
    await userEvent.click(candidate);
    await userEvent.click(screen.getByRole("button", { name: "Relink" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const relink = sent.find((r) => r.key === "POST /activities/act-1/relink");
    expect(relink?.body).toEqual({
      entity_type: "deal",
      entity_id: "d-9",
      replace_existing_of_type: false,
    });
    // Relink is idempotency-keyed (its no-dup-on-replay contract).
    expect(relink?.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("sends replace_existing_of_type when the move toggle is on", async () => {
    const onClose = vi.fn();
    const sent = stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [{ type: "organization", id: "o-2", title: "Globex" }],
          page: { has_more: false },
        }),
      "POST /activities/act-1/relink": () => jsonResponse(activity202),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="deal"
        entityId="d-1"
        open
        onClose={onClose}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "Globex");
    await userEvent.click(
      await screen.findByRole("button", { name: "Globex" }),
    );
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(screen.getByRole("button", { name: "Relink" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const relink = sent.find((r) => r.key === "POST /activities/act-1/relink");
    expect(relink?.body).toEqual({
      entity_type: "organization",
      entity_id: "o-2",
      replace_existing_of_type: true,
    });
  });

  it("drops activity results — relink has no activity target", async () => {
    stubRoutes({
      "GET /search": () =>
        jsonResponse({
          data: [
            { type: "activity", id: "a-x", title: "Some email" },
            { type: "person", id: "pp-1", title: "Jane Doe" },
          ],
          page: { has_more: false },
        }),
    });
    render(
      <RelinkModal
        activityId="act-1"
        entityType="deal"
        entityId="d-1"
        open
        onClose={vi.fn()}
      />,
    );

    await userEvent.type(screen.getByRole("searchbox"), "e");
    expect(
      await screen.findByRole("button", { name: "Jane Doe" }),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Some email" })).toBeNull();
  });
});

// Fills the four Send preconditions (To, subject, body, purpose) so a test can
// then exercise the send outcome under study.
async function fillSendableForm() {
  await userEvent.type(screen.getByLabelText("To"), "a@x.com");
  await userEvent.tab();
  await userEvent.type(screen.getByPlaceholderText("Subject"), "Hi there");
  await userEvent.type(screen.getByPlaceholderText("Body"), "Body content");
  // The purpose <option> value is the ConsentPurpose.key the wire sends.
  await userEvent.selectOptions(screen.getByRole("combobox"), "transactional");
}

describe("ComposeModal", () => {
  it("fills To/Subject/Body from the AI draft", async () => {
    stubRoutes({
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          to: ["buyer@acme.test"],
        }),
    });
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );

    // getByDisplayValue reads the field's current value without a DOM cast.
    expect(await screen.findByDisplayValue("Re: Q3 numbers")).toBeTruthy();
    expect(screen.getByDisplayValue("Thanks for the note.")).toBeTruthy();
    // EmailDraft.to prefills the recipient chips.
    expect(screen.getByText("buyer@acme.test")).toBeTruthy();
  });

  it("shows an unavailable note on a 501 draft, keeping the form usable", async () => {
    stubRoutes({
      "POST /activities/act-1/draft-email": () => emptyResponse(501),
    });
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={vi.fn()}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );

    expect(await screen.findByText(/AI drafting is unavailable/i)).toBeTruthy();
    // Manual composing still works — Send is present.
    expect(screen.getByRole("button", { name: "Send" })).toBeTruthy();
  });

  it("keeps Send disabled until To, subject, body, and purpose are set", async () => {
    stubRoutes();
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("combobox");

    expect(
      screen.getByRole("button", { name: "Send" }).hasAttribute("disabled"),
    ).toBe(true);
    await fillSendableForm();
    expect(
      screen.getByRole("button", { name: "Send" }).hasAttribute("disabled"),
    ).toBe(false);
  });

  it("sends the edited email with no approval token or idempotency key", async () => {
    const onClose = vi.fn();
    const sent = stubRoutes({
      "POST /activities/act-1/send-email": () => jsonResponse(activity202, 202),
    });
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={onClose}
      />,
    );
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const req = sent.find((r) => r.key === "POST /activities/act-1/send-email");
    expect(req?.body).toEqual({
      subject: "Hi there",
      body: "Body content",
      to: ["a@x.com"],
      consent_purpose: "transactional",
    });
    // ADR-0055: the human click is the approval — neither header rides along.
    expect(req?.headers.get("X-Approval-Token")).toBeNull();
    expect(req?.headers.get("Idempotency-Key")).toBeNull();
  });

  it("surfaces the default-deny consent gate on 409 without closing", async () => {
    const onClose = vi.fn();
    stubRoutes({
      "POST /activities/act-1/send-email": () =>
        problemResponse(
          {
            code: "consent_not_granted",
            detail: "suppressed",
            title: "Conflict",
          },
          409,
        ),
    });
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        personId="p-1"
        open
        onClose={onClose}
      />,
    );
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText(/has not granted consent/i)).toBeTruthy();
    // The gate points at the consent surface, and the modal stays open.
    expect(screen.getByRole("link", { name: "Review consent" })).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows a sending-unavailable note on a 501 send, not an error", async () => {
    const onClose = vi.fn();
    stubRoutes({
      "POST /activities/act-1/send-email": () => emptyResponse(501),
    });
    render(
      <ComposeModal
        activityId="act-1"
        entityType="person"
        entityId="p-1"
        open
        onClose={onClose}
      />,
    );
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText(/Sending is unavailable/i)).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("TimelineActions", () => {
  const emailLinked: Activity = {
    ...activity202,
    id: "a1",
    kind: "email",
    links: [{ entity_type: "deal", entity_id: "d1" }],
  };
  const noteLinked: Activity = {
    ...activity202,
    id: "a2",
    kind: "note",
    links: [{ entity_type: "deal", entity_id: "d1" }],
  };
  const noteBare: Activity = {
    ...activity202,
    id: "a3",
    kind: "note",
    links: [],
  };

  it("offers Reply on email rows and Relink on linked rows", () => {
    stubRoutes();
    render(
      <TimelineActions
        activity={emailLinked}
        entityType="deal"
        entityId="d1"
      />,
    );
    expect(screen.getByRole("button", { name: "Reply" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Relink" })).toBeTruthy();
  });

  it("offers Relink but not Reply on a linked non-email row", () => {
    stubRoutes();
    render(
      <TimelineActions activity={noteLinked} entityType="deal" entityId="d1" />,
    );
    expect(screen.queryByRole("button", { name: "Reply" })).toBeNull();
    expect(screen.getByRole("button", { name: "Relink" })).toBeTruthy();
  });

  it("renders nothing for a bare note with no links", () => {
    stubRoutes();
    render(
      <TimelineActions activity={noteBare} entityType="deal" entityId="d1" />,
    );
    expect(screen.queryByRole("button", { name: "Reply" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Relink" })).toBeNull();
  });

  it("opens the composer when Reply is clicked", async () => {
    stubRoutes();
    render(
      <TimelineActions
        activity={emailLinked}
        entityType="deal"
        entityId="d1"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Reply" }));
    // The ConfirmModal titled "Send this email?" mounts only once Reply opens it.
    expect(await screen.findByText("Send this email?")).toBeTruthy();
  });
});
