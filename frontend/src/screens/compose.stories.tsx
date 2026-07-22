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
};

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

// "Draft with AI" fills To/Subject/Body from the returned EmailDraft.
export const Drafted: Story = {
  render: composeStory({
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
