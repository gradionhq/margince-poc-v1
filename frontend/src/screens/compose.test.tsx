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

// Two purposes, because the send rules differ by purpose: transactional is the
// one locked, unsubscribe-free lane; anything else renders a per-recipient
// unsubscribe link.
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
    {
      id: "p2",
      workspace_id: "w",
      key: "marketing_email",
      label: "Marketing email",
      requires_double_opt_in: true,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// listVoiceProfiles caps at one profile and answers an empty page when the
// caller has none — the state most composer tests run in.
const NO_VOICE_PROFILE = {
  data: [],
  page: { next_cursor: null, has_more: false },
};

// One profile whose maturity is the middle band (800–4000 corpus words): enough
// to style a draft, not yet a full build.
const PROVISIONAL_VOICE_PROFILE = {
  data: [
    {
      id: "vp-1",
      owner_id: "u1",
      status: "ready",
      maturity: "provisional",
      quality_band: "thin",
      voice_profile_md: "Short sentences.",
      profile_version: 3,
      personality_md: "",
      auto_learning_enabled: false,
      active_source_hash: null,
      candidate_version: null,
      last_built_at: null,
      source: "manual",
      captured_by: "human:u1",
      version: 1,
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
      archived_at: null,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// rejectVoiceDraft answers the owner's updated learning aggregate; the composer
// only needs the call to have succeeded.
const LEARNING_SUMMARY = {
  drafted: 4,
  accepted: 1,
  edited_sent: 2,
  rejected: 1,
  qualifying_source_count: 0,
  qualifying_words: 0,
  transformations: [],
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
      if (key === "GET /voice-profiles") return jsonResponse(NO_VOICE_PROFILE);
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

  // Art. 50 is a hard gate: a model-produced draft that reaches a human
  // without a disclosure is a compliance failure, so these three cases fix the
  // banner's presence, its verbatim text, and its absence on human-written
  // text. Removing the banner from the composer fails all three.
  it("discloses a model-produced draft, rendering the server's line verbatim", async () => {
    stubRoutes({
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          ai_generated: true,
          ai_disclosure: "AI-assisted draft (Art. 50): reviewed by a human.",
          draft_ref: null,
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

    expect(await screen.findByTestId("ai-disclosure-banner")).toBeTruthy();
    expect(
      screen.getByText("AI-assisted draft (Art. 50): reviewed by a human."),
    ).toBeTruthy();
  });

  it("still discloses when the server sends no disclosure line", async () => {
    // ai_disclosure is contract-guaranteed alongside ai_generated, but a
    // client that trusts that would drop the disclosure entirely against an
    // older or misbehaving server. Absence of the line is not absence of the
    // obligation, so the composer carries its own wording.
    stubRoutes({
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          ai_generated: true,
          ai_disclosure: null,
          draft_ref: null,
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

    expect(await screen.findByTestId("ai-disclosure-banner")).toBeTruthy();
    expect(screen.getByText(/This draft was produced by AI/i)).toBeTruthy();
  });

  it("discloses nothing when no model produced the draft", async () => {
    stubRoutes({
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          ai_generated: false,
          ai_disclosure: null,
          draft_ref: null,
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

    // The fill proves the draft landed, so the missing banner is the
    // disclosure being conditional rather than the response never arriving.
    expect(await screen.findByDisplayValue("Re: Q3 numbers")).toBeTruthy();
    expect(screen.queryByTestId("ai-disclosure-banner")).toBeNull();
  });

  it("names the voice version that styled the draft and flags a provisional profile", async () => {
    stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          ai_generated: true,
          ai_disclosure: "AI-assisted draft (Art. 50).",
          voice_profile_version: 3,
          draft_ref: "vd-1",
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

    expect(await screen.findByText("Built from your corpus · v3")).toBeTruthy();
    expect(screen.getByText("Provisional voice")).toBeTruthy();
  });

  it("flags nothing provisional when the profile is past that band", async () => {
    stubRoutes({
      "GET /voice-profiles": () =>
        jsonResponse({
          ...PROVISIONAL_VOICE_PROFILE,
          data: [
            { ...PROVISIONAL_VOICE_PROFILE.data[0], maturity: "building" },
          ],
        }),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse({
          subject: "Re: Q3 numbers",
          body: "Thanks for the note.",
          ai_generated: true,
          ai_disclosure: "AI-assisted draft (Art. 50).",
          voice_profile_version: 3,
          draft_ref: "vd-1",
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

    expect(await screen.findByText("Built from your corpus · v3")).toBeTruthy();
    expect(screen.queryByText("Provisional voice")).toBeNull();
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
    // Nothing was drafted, so no voice-learning outcome may be inferred: the
    // exact-match assertion is what keeps `draft_ref` off an independently
    // composed send rather than riding along as null.
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

  it("does not report a send on a bodiless non-2xx (gateway 5xx)", async () => {
    const onClose = vi.fn();
    // openapi-fetch yields a falsy error for a bodiless response; success must
    // be the real 202, so an empty 503 stays an error and keeps the modal open
    // rather than falsely confirming an irreversible send.
    stubRoutes({
      "POST /activities/act-1/send-email": () => emptyResponse(503),
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

    expect(await screen.findByText(/The request failed/i)).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows a draft error on a bodiless non-2xx without crashing the fill", async () => {
    // The old !error success path would fabricate an undefined draft and crash
    // onSuccess reading its fields; a bodiless 502 must surface an error only.
    stubRoutes({
      "POST /activities/act-1/draft-email": () => emptyResponse(502),
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
    await screen.findByRole("combobox");
    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );

    expect(await screen.findByText(/The request failed/i)).toBeTruthy();
  });
});

// A voice-styled draft, served with the reference the server keys a learning
// outcome against.
function voiceDraft(ref: string, subject: string, body: string) {
  return {
    subject,
    body,
    to: ["buyer@acme.test"],
    ai_generated: true,
    ai_disclosure: "AI-assisted draft (Art. 50).",
    voice_profile_version: 3,
    draft_ref: ref,
  };
}

// Serves a different draft per call, so a test can re-draft and see which of
// the two references the composer is still holding.
function draftsInTurn(...drafts: object[]) {
  let call = 0;
  return () => {
    const drafted = drafts[Math.min(call, drafts.length - 1)];
    call += 1;
    return jsonResponse(drafted);
  };
}

function renderComposer(onClose = vi.fn()) {
  render(
    <ComposeModal
      activityId="act-1"
      entityType="person"
      entityId="p-1"
      open
      onClose={onClose}
    />,
  );
  return onClose;
}

// The send-time binding to the voice draft: `draft_ref` is what makes the
// server's accepted/edited_sent verdict about the rep's own words.
describe("ComposeModal draft binding", () => {
  it("sends the reference of the draft whose text it is sending", async () => {
    const sent = stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse(voiceDraft("vd-a", "Re: Q3", "Draft A body.")),
      "POST /activities/act-1/send-email": () => jsonResponse(activity202, 202),
    });
    renderComposer();
    await screen.findByRole("combobox");

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    await screen.findByDisplayValue("Draft A body.");
    await userEvent.selectOptions(
      screen.getByRole("combobox"),
      "transactional",
    );
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(
        sent.some((r) => r.key === "POST /activities/act-1/send-email"),
      ).toBe(true),
    );
    const req = sent.find((r) => r.key === "POST /activities/act-1/send-email");
    expect(req?.body).toMatchObject({
      body: "Draft A body.",
      draft_ref: "vd-a",
    });
  });

  it("keeps the earlier reference when a re-draft leaves the typed body alone", async () => {
    // The fill rule never clobbers text the rep has touched, so the second
    // draft's words are discarded. Adopting its reference anyway would submit
    // draft B's identity with draft A's text plus the rep's edit — an
    // edited_sent whose "edit" is a draft the rep never saw.
    const sent = stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": draftsInTurn(
        voiceDraft("vd-a", "Re: Q3", "Draft A body."),
        voiceDraft("vd-b", "Re: Q3 again", "Draft B body."),
      ),
      "POST /activities/act-1/send-email": () => jsonResponse(activity202, 202),
    });
    renderComposer();
    await screen.findByRole("combobox");

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    const bodyField = await screen.findByDisplayValue("Draft A body.");
    await userEvent.type(bodyField, " And my own line.");
    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    await waitFor(() =>
      expect(
        sent.filter((r) => r.key === "POST /activities/act-1/draft-email")
          .length,
      ).toBe(2),
    );

    await userEvent.selectOptions(
      screen.getByRole("combobox"),
      "transactional",
    );
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(
        sent.some((r) => r.key === "POST /activities/act-1/send-email"),
      ).toBe(true),
    );
    const req = sent.find((r) => r.key === "POST /activities/act-1/send-email");
    expect(req?.body).toMatchObject({
      body: "Draft A body. And my own line.",
      draft_ref: "vd-a",
    });
  });

  it("drops the reference once the body it named is cleared", async () => {
    const sent = stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse(voiceDraft("vd-a", "Re: Q3", "Draft A body.")),
      "POST /activities/act-1/send-email": () => jsonResponse(activity202, 202),
    });
    renderComposer();
    await screen.findByRole("combobox");

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    const bodyField = await screen.findByDisplayValue("Draft A body.");
    await userEvent.clear(bodyField);
    await userEvent.type(bodyField, "Written from scratch.");
    await userEvent.selectOptions(
      screen.getByRole("combobox"),
      "transactional",
    );
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(
        sent.some((r) => r.key === "POST /activities/act-1/send-email"),
      ).toBe(true),
    );
    const req = sent.find((r) => r.key === "POST /activities/act-1/send-email");
    expect(req?.body).not.toHaveProperty("draft_ref");
  });

  it("records exactly one rejection when the rep discards the draft", async () => {
    const sent = stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse(voiceDraft("vd-a", "Re: Q3", "Draft A body.")),
      "POST /voice-profiles/vp-1/draft-rejections": () =>
        jsonResponse(LEARNING_SUMMARY),
    });
    renderComposer();
    await screen.findByRole("combobox");

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    await screen.findByDisplayValue("Draft A body.");
    await userEvent.click(
      screen.getByRole("button", { name: "Discard draft" }),
    );

    await waitFor(() =>
      expect(screen.queryByDisplayValue("Draft A body.")).toBeNull(),
    );
    const rejections = sent.filter(
      (r) => r.key === "POST /voice-profiles/vp-1/draft-rejections",
    );
    expect(rejections).toHaveLength(1);
    expect(rejections[0].body).toEqual({ draft_ref: "vd-a" });
  });

  it("records no rejection when the composer is merely closed", async () => {
    // `rejected` is a judgment, not an accident of navigation — and because the
    // reference is deterministic and the drafted signal inserts once, a
    // rejection logged on a close would stand in for the real outcome of an
    // identical draft that is later sent.
    const sent = stubRoutes({
      "GET /voice-profiles": () => jsonResponse(PROVISIONAL_VOICE_PROFILE),
      "POST /activities/act-1/draft-email": () =>
        jsonResponse(voiceDraft("vd-a", "Re: Q3", "Draft A body.")),
    });
    const onClose = renderComposer();
    await screen.findByRole("combobox");

    await userEvent.click(
      screen.getByRole("button", { name: "Draft with AI" }),
    );
    await screen.findByDisplayValue("Draft A body.");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onClose).toHaveBeenCalled();
    expect(sent.some((r) => r.key.includes("draft-rejections"))).toBe(false);
  });
});

// The send pre-flight's two refusals: each is a product state with a fix, so
// each must read as its own copy rather than as the generic failure line the
// server's detail string would otherwise land in.
describe("ComposeModal send refusals", () => {
  it("tells the rep to reconnect a capture-only mailbox, and where", async () => {
    const onClose = vi.fn();
    stubRoutes({
      "POST /activities/act-1/send-email": () =>
        problemResponse(
          {
            code: "mailbox_not_send_capable",
            title: "Unprocessable Entity",
            detail: "opaque server wording",
          },
          422,
        ),
    });
    renderComposer(onClose);
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(
      await screen.findByText(/never granted permission to send/i),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "Reconnect your mailbox" })
        .getAttribute("href"),
    ).toBe("#/settings/integrations");
    // The refusal replaces the generic line rather than joining it.
    expect(screen.queryByText("opaque server wording")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("states the one-send-per-recipient rule on a shared unsubscribe token", async () => {
    stubRoutes({
      "POST /activities/act-1/send-email": () =>
        problemResponse(
          {
            code: "shared_unsubscribe_token",
            title: "Unprocessable Entity",
            detail: "opaque server wording",
          },
          422,
        ),
    });
    renderComposer();
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(
      await screen.findByText(/reaches one addressee at a time/i),
    ).toBeTruthy();
    expect(screen.queryByText("opaque server wording")).toBeNull();
  });

  it("keeps the generic line for a refusal it has no copy for", async () => {
    stubRoutes({
      "POST /activities/act-1/send-email": () =>
        problemResponse(
          {
            code: "some_future_refusal",
            title: "Unprocessable Entity",
            detail: "opaque server wording",
          },
          422,
        ),
    });
    renderComposer();
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText(/opaque server wording/i)).toBeTruthy();
  });

  it("warns about a second addressee before the send, not after it", async () => {
    // Every purpose but transactional renders one recipient's unsubscribe link,
    // so the server refuses a second addressee outright. Saying so after the
    // round trip is strictly worse than saying so while the rep can still fix it.
    const sent = stubRoutes();
    renderComposer();
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.type(screen.getByLabelText("Cc"), "second@x.com");
    await userEvent.tab();

    expect(screen.queryByText(/more than one addressee/i)).toBeNull();
    await userEvent.selectOptions(
      screen.getByRole("combobox"),
      "marketing_email",
    );

    expect(await screen.findByText(/more than one addressee/i)).toBeTruthy();
    // A warning, not a gate — and nothing was sent to earn it.
    expect(
      sent.some((r) => r.key === "POST /activities/act-1/send-email"),
    ).toBe(false);
  });

  it("does not warn about a lone addressee under the same purpose", async () => {
    stubRoutes();
    renderComposer();
    await screen.findByRole("combobox");
    await fillSendableForm();
    await userEvent.selectOptions(
      screen.getByRole("combobox"),
      "marketing_email",
    );

    expect(screen.queryByText(/more than one addressee/i)).toBeNull();
  });
});

describe("TimelineActions", () => {
  const email: Activity = {
    ...activity202,
    id: "a1",
    kind: "email",
  };
  const note: Activity = {
    ...activity202,
    id: "a2",
    kind: "note",
  };

  it("offers Reply on an email row and Relink alongside it", () => {
    stubRoutes();
    render(
      <TimelineActions activity={email} entityType="deal" entityId="d1" />,
    );
    expect(screen.getByRole("button", { name: "Reply" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Relink" })).toBeTruthy();
  });

  it("offers Reply on a non-email row too, as the send path allows", () => {
    // A send anchored to a note carries no RFC822 identity and starts a
    // conversation, which the backend handles. Gating this on kind === "email"
    // is what made "log a note → compose → send" unreachable in a workspace
    // whose timeline holds nothing captured from mail.
    stubRoutes();
    render(<TimelineActions activity={note} entityType="deal" entityId="d1" />);
    expect(screen.getByRole("button", { name: "Reply" })).toBeTruthy();
    // Relink is always available — the row is already linked to this timeline's
    // entity, and the Activity list payload carries no `links` to gate on.
    expect(screen.getByRole("button", { name: "Relink" })).toBeTruthy();
  });

  it("opens the composer anchored to a note row", async () => {
    stubRoutes();
    render(<TimelineActions activity={note} entityType="deal" entityId="d1" />);
    await userEvent.click(screen.getByRole("button", { name: "Reply" }));
    expect(await screen.findByText("Send this email?")).toBeTruthy();
  });

  it("opens the composer when Reply is clicked", async () => {
    stubRoutes();
    render(
      <TimelineActions activity={email} entityType="deal" entityId="d1" />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Reply" }));
    // The ConfirmModal titled "Send this email?" mounts only once Reply opens it.
    expect(await screen.findByText("Send this email?")).toBeTruthy();
  });
});
