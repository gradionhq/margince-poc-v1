// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ShareScreen } from "./share";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ShareScreen (AS-3/4/5) — record-level manual grants (A52/ADR-0039):
// empty roster (nothing to grant to yet), a populated who-has-access list,
// the revoke confirm modal opened, and the four states a subject who ALREADY
// holds a grant introduced once the picker stopped refusing them — the level
// on the row, the reduce-access confirm, the honest "nothing changed", and the
// recipient's seat ceiling refusing a write.

const meta: Meta = {
  title: "Patterns/Share record",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const usersPage = {
  data: [
    { id: "u-1", display_name: "Priya Nair", email: "priya@example.com" },
    { id: "u-2", display_name: "Mor Adler", email: "mor@example.com" },
  ],
  page: { next_cursor: null, has_more: false },
};

const teamsPage = {
  data: [{ id: "t-1", name: "Deal Desk", member_count: 4 }],
  page: { next_cursor: null, has_more: false },
};

// The header renders <EntityRef kind="deal" id="d-1"> — without this stub the
// story shows the bare id, which isn't reviewable. Stub the record read so the
// name resolves exactly as it would in the app.
const deal = () =>
  jsonResponse({ id: "d-1", name: "BÄR Pharma — Packaging QA" });

const grant = {
  id: "g-1",
  record_type: "deal",
  record_id: "d-1",
  subject_type: "user" as const,
  subject_id: "u-2",
  access: "read" as const,
  granted_by: "u-1",
  reason: "compliance review",
  expires_at: null,
  created_at: "2026-06-22T14:08:00Z",
  version: 1,
};

// The same grant one level up: the picker row and the who-has-access row both
// draw write in the accent tone, where read is the neutral badge.
const writeGrant = { ...grant, access: "write" as const };

// Either level, so one set of routes serves both — the fixtures fix their own
// `access` literal, and a story seeds the grant it is about.
type HeldGrant = typeof grant | typeof writeGrant;

// The routes every already-granted story shares: one grant on the record, and
// a POST that answers the way an idempotent create does — the same row
// restated, never a second grant. The screen reads nothing out of that body
// (it invalidates the list instead), so the answer is the row itself.
const reassertRoutes = (held: HeldGrant) => ({
  "GET /users": () => jsonResponse(usersPage),
  "GET /teams": () => jsonResponse(teamsPage),
  "GET /deals/d-1": deal,
  "GET /record-grants": () =>
    jsonResponse({
      data: [held],
      page: { next_cursor: null, has_more: false },
    }),
  "POST /record-grants": () => jsonResponse(held, 201),
});

// Pick the subject who already holds a grant. Its row carries the held level
// as a badge, which is what the picker used to hide behind a disabled row.
async function pickHolder(canvas: ReturnType<typeof within>) {
  await userEvent.click(
    await canvas.findByRole("button", { name: /Mor Adler/ }),
  );
}

export const EmptyRoster: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /teams": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /deals/d-1": deal,
      "GET /record-grants": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
};

export const WithAccessList: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () => jsonResponse(usersPage),
      "GET /teams": () => jsonResponse(teamsPage),
      "GET /deals/d-1": deal,
      "GET /record-grants": () =>
        jsonResponse({
          data: [grant],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
};

export const RevokeConfirmOpen: Story = {
  render: () => {
    installFetchStub({
      "GET /users": () => jsonResponse(usersPage),
      "GET /teams": () => jsonResponse(teamsPage),
      "GET /deals/d-1": deal,
      "GET /record-grants": () =>
        jsonResponse({
          data: [grant],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const revokeButton = await canvas.findByTestId("revoke-grant");
    await userEvent.click(revokeButton);
  },
};

// A subject holding WRITE, picked: the accent badge on their row, the same
// badge in the list below, and the form opened on what they hold rather than
// on the compose defaults.
export const HolderPicked: Story = {
  render: () => {
    installFetchStub(reassertRoutes(writeGrant));
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await pickHolder(within(canvasElement));
  },
};

// Reducing a colleague's level, asked before it happens: the one direction the
// actor may not have meant.
export const ReduceAccessConfirm: Story = {
  render: () => {
    installFetchStub(reassertRoutes(writeGrant));
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await pickHolder(canvas);
    await userEvent.click(canvas.getByRole("button", { name: "Read" }));
    await userEvent.click(canvas.getByTestId("share-grant-submit"));
  },
};

// A re-assert that moved nothing. The list below is identical afterwards, so
// the screen says so instead of letting silence read as a change that landed.
export const NothingChanged: Story = {
  render: () => {
    installFetchStub(reassertRoutes(grant));
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await pickHolder(canvas);
    await userEvent.click(canvas.getByTestId("share-grant-submit"));
  },
};

// 403 seat_tier_insufficient: the ceiling binds the RECIPIENT's licence, so the
// refusal names their seat rather than reading as a permission problem of the
// actor's own.
export const SeatCeilingRefusal: Story = {
  render: () => {
    installFetchStub({
      ...reassertRoutes(grant),
      "POST /record-grants": () =>
        jsonResponse(
          {
            type: "about:blank",
            title: "Forbidden",
            status: 403,
            code: "seat_tier_insufficient",
            detail: "the seat ceiling refuses a write grant",
          },
          403,
        ),
    });
    return (
      <StoryProviders>
        <ShareScreen recordType="deal" recordId="d-1" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /Priya Nair/ }),
    );
    await userEvent.click(canvas.getByRole("button", { name: "Write" }));
    await userEvent.click(canvas.getByTestId("share-grant-submit"));
  },
};
