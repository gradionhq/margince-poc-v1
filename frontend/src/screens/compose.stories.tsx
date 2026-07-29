// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { ComposeModal, RelinkModal } from "./compose";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The composer surface (draftEmail / sendEmail / relinkActivity) rendered off
// the same fixture shapes compose.test.tsx exercises — never a live call. The
// interesting states are reachable only through the form (draft, send-confirm),
// so each story that needs one drives it in `play` with the same userEvent
// steps the unit tests use, keeping the captured frame faithful to a real run.

// One consent purpose is enough to satisfy the Send precondition and populate
// the purpose <select>; its `key` ("transactional") is the wire value a send
// carries. Mirrors compose.test.tsx's PURPOSES, not an invented shape.
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

const DRAFT: components["schemas"]["EmailDraft"] = {
  subject: "Re: Q3 numbers",
  body: "Thanks for the note — following up as promised.",
  to: ["buyer@acme.test"],
  ai_generated: true,
  ai_disclosure: "AI-assisted draft (Art. 50): reviewed and sent by a human.",
  // A voice-styled draft: the profile version is the provenance the banner
  // reports, and the reference is what a send or a discard binds its outcome
  // to. Both are null on a draft no voice profile shaped.
  voice_profile_version: 3,
  draft_ref: "vd-1",
};

// The owner's profile behind that draft, in the middle maturity band — the
// state that adds the provisional label to the disclosure banner.
const VOICE_PROFILE = {
  data: [
    {
      id: "vp-1",
      owner_id: "u1",
      status: "ready",
      maturity: "provisional",
      quality_band: "thin",
      voice_profile_md: "Short sentences. Concrete nouns.",
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

// The 422 body httperr.Validation actually emits: the top-level `code` is the
// same for every validation problem, and the rule that fired is named per
// field under details.errors. A story that hoisted the specific code to the
// top level would frame a refusal the server cannot produce.
function validationProblem(field: string, code: string, message: string) {
  return {
    code: "validation_error",
    title: "Unprocessable Entity",
    detail: message,
    details: { errors: [{ field, code, message }] },
  };
}

// Renders the composer over a given route map, always serving the consent
// purposes the purpose selector needs on top of the story's own routes.
function composeStory(routes: RouteMap) {
  return () => {
    installFetchStub({
      "GET /consent-purposes": () => jsonResponse(PURPOSES),
      ...routes,
    });
    return (
      <StoryProviders>
        <ComposeModal
          activityId="act-1"
          entityType="person"
          entityId="p-1"
          personId="p-1"
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  };
}

// Fills the four Send preconditions (To, subject, body, purpose) then confirms
// — the same sequence fillSendableForm drives in compose.test.tsx, so a story
// reaches the send outcome (409 gate / 501 unavailable) it means to capture.
async function fillAndSend(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.type(canvas.getByLabelText("To"), "buyer@acme.test");
  await userEvent.tab();
  await userEvent.type(canvas.getByPlaceholderText("Subject"), "Following up");
  await userEvent.type(canvas.getByPlaceholderText("Body"), "As promised.");
  await userEvent.selectOptions(canvas.getByRole("combobox"), "transactional");
  await userEvent.click(canvas.getByRole("button", { name: "Send" }));
}

const meta: Meta = {
  title: "screens/compose",
};
export default meta;

type Story = StoryObj;

// The composer as it opens: empty fields, the draft bar, and Send disabled
// until the four preconditions are met.
export const Empty: Story = {
  render: composeStory({}),
};

// "Draft with AI" fills To/Subject/Body from the returned EmailDraft and
// discloses it: the Art. 50 banner, the voice version that styled it, and the
// provisional label its profile currently carries.
export const Drafted: Story = {
  render: composeStory({
    "GET /voice-profiles": () => jsonResponse(VOICE_PROFILE),
    "POST /activities/act-1/draft-email": () => jsonResponse(DRAFT),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: "Draft with AI" }),
    );
  },
};

// The default-deny consent gate (A22/ADR-0011): a filled, confirmed send comes
// back 409 consent_not_granted, so the modal stays open with the pointed
// "Review consent" copy instead of a raw server error.
export const ConsentBlocked: Story = {
  render: composeStory({
    "POST /activities/act-1/send-email": () =>
      jsonResponse(
        {
          code: "consent_not_granted",
          title: "Conflict",
          detail: "suppressed",
        },
        409,
      ),
  }),
  play: async ({ canvasElement }) => {
    await fillAndSend(canvasElement);
  },
};

// The mailbox is connected for capture but holds no send grant (422). The
// refusal names the only fix — reconnect — and links to the connect surface,
// because the provider will not widen an existing grant in place.
export const MailboxNotSendCapable: Story = {
  render: composeStory({
    "POST /activities/act-1/send-email": () =>
      jsonResponse(
        validationProblem(
          "from",
          "mailbox_not_send_capable",
          "reconnect your mailbox to enable sending",
        ),
        422,
      ),
  }),
  play: async ({ canvasElement }) => {
    await fillAndSend(canvasElement);
  },
};

// An unsubscribe link carries one recipient's own consent credential, so a
// send addressed to more than one recipient is refused outright (422). The
// refusal states the one-address-at-a-time rule instead of the opaque server
// wording.
export const SharedUnsubscribeToken: Story = {
  render: composeStory({
    "POST /activities/act-1/send-email": () =>
      jsonResponse(
        validationProblem(
          "recipients",
          "shared_unsubscribe_token",
          "reaches one addressee at a time",
        ),
        422,
      ),
  }),
  play: async ({ canvasElement }) => {
    await fillAndSend(canvasElement);
  },
};

// No mailer configured: the send answers 501, surfaced as an honest inline
// "Sending is unavailable" note, never thrown into the error channel.
export const SendUnavailable: Story = {
  render: composeStory({
    "POST /activities/act-1/send-email": () =>
      jsonResponse(
        { title: "Not Implemented", detail: "mailer not wired" },
        501,
      ),
  }),
  play: async ({ canvasElement }) => {
    await fillAndSend(canvasElement);
  },
};

// The relink dialog: a cross-object /search returns a few candidates that the
// RecordPicker lists once the user types a query.
export const Default: Story = {
  render: () => {
    installFetchStub({
      "GET /search": () =>
        jsonResponse({
          data: [
            { type: "deal", id: "d-9", title: "Acme renewal" },
            { type: "organization", id: "o-2", title: "Acme GmbH" },
            { type: "person", id: "pp-1", title: "Jane Doe" },
          ],
          page: { has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <RelinkModal
          activityId="act-1"
          entityType="person"
          entityId="p-1"
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByRole("searchbox"), "Acme");
  },
};
