// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { AiCallsCard, CallDetailPanel } from "./aicalls";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The card is gated on automation:update, so /me decides which of its two
// branches renders. Left unrouted, the fetch stub answers with an empty list
// page, useMe rejects that as malformed, and every grant fails closed — which
// is how the List and Empty stories below both used to draw the same probe
// error under two names that promised the trace table.
const OPERATOR: GrantSpec = { automation: ["read", "update"] };

const summary = {
  id: "call-1",
  occurred_at: "2026-07-20T10:00:00Z",
  task: "capture_classify",
  tier: "cheap_cloud",
  provider: "gemini",
  model_id: "configured",
  served_model: "served",
  calls_attempted: 2,
  tokens_in: 100,
  tokens_out: 20,
  reasoning_tokens: 0,
  cached_tokens: 0,
  latency_ms: 900,
  cache_hit: false,
  degraded: true,
  error_sentinel: "provider_unavailable",
  has_payload: true,
};
const detail = {
  ...summary,
  served_identity_source: "response",
  context_scopes: ["identity"],
  context_fingerprint: "abc",
  attempts: [
    {
      attempt: 1,
      is_terminal: false,
      attempt_reason: "",
      tokens_in: 100,
      tokens_out: 0,
      latency_ms: 400,
      occurred_at: summary.occurred_at,
    },
    {
      attempt: 2,
      is_terminal: true,
      attempt_reason: "retry_on_5xx",
      tokens_in: 100,
      tokens_out: 20,
      latency_ms: 900,
      occurred_at: summary.occurred_at,
    },
  ],
  payload_captured: true,
  payload: { request: { system: "safe", messages: [] }, response: "ok" },
};

function list(data: unknown[], capture = true, allow: GrantSpec = OPERATOR) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow })),
      "GET /ai/calls": () =>
        jsonResponse({
          data,
          page: { has_more: false },
          payload_capture_enabled: capture,
          tasks: ["capture_classify"],
        }),
      "GET /ai/calls/call-1": () => jsonResponse(detail),
    });
    return (
      <StoryProviders>
        <AiCallsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof AiCallsCard> = {
  title: "Settings/Admin settings/AI/Model calls",
  component: AiCallsCard,
};
export default meta;
type Story = StoryObj<typeof AiCallsCard>;
export const List: Story = { render: list([summary]) };
export const Empty: Story = { render: list([]) };

// No automation grant: the trace keeps its place and says it is withheld. An
// absent card would read as "this installation made no model calls".
export const Withheld: Story = { render: list([summary], true, {}) };

// A page that has a next one, which is the only state that draws the pager. The
// trace row stacks its control as a COLUMN — `.settingrow-control` is a flex row
// — so this is the story that says whether Load more sits under the table or
// beside it.
export const ListPaged: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ allow: OPERATOR })),
      "GET /ai/calls": () =>
        jsonResponse({
          data: [summary],
          page: { has_more: true, next_cursor: "page-2" },
          payload_capture_enabled: true,
          tasks: ["capture_classify"],
        }),
      "GET /ai/calls/call-1": () => jsonResponse(detail),
    });
    return (
      <StoryProviders>
        <AiCallsCard />
      </StoryProviders>
    );
  },
};

export const PayloadOff: Story = {
  render: () => {
    installFetchStub({
      "GET /ai/calls/call-1": () =>
        jsonResponse({ ...detail, payload_captured: false, payload: null }),
    });
    return (
      <StoryProviders>
        <CallDetailPanel id="call-1" captureEnabled={false} />
      </StoryProviders>
    );
  },
};
export const WithPayload: Story = {
  render: () => {
    installFetchStub({ "GET /ai/calls/call-1": () => jsonResponse(detail) });
    return (
      <StoryProviders>
        <CallDetailPanel id="call-1" captureEnabled />
      </StoryProviders>
    );
  },
};

// The disclosure button opens the attempt trail under its own row, so the trace
// stays readable as one thing rather than two surfaces side by side. Shared by
// the stories below, which all need the row OPEN to show what they are about.
const openAttemptTrail: NonNullable<Story["play"]> = async ({
  canvasElement,
}) => {
  const canvas = within(canvasElement);
  const disclosure = await canvas.findByRole("button", {
    name: /Show the attempt trail for capture_classify/,
  });
  await userEvent.click(disclosure);
  await canvas.findByText("Attempts");
};

// The detail panel IN the table, which is the only place a reader meets it.
export const RowExpanded: Story = {
  render: list([summary]),
  play: openAttemptTrail,
};

// The same expanded row in dark. Two badge tones are all that separates a call
// that limped from one that failed — `degraded` (warn) and the error sentinel
// (danger) — so a tint that stops carrying that distinction against a dark card
// takes the task column's meaning with it. The trail is open because the
// attempt table brings a second danger badge onto a nested surface, where a
// translucent tone composites over a different ground than on the card face.
export const RowExpandedDark: Story = {
  globals: { theme: "dark" },
  render: list([summary]),
  play: openAttemptTrail,
};

// Six columns of trace at 390px. None of them is droppable — a call is only
// diagnosable with its model, tokens and latency side by side — so the card is
// meant to scroll sideways inside itself (`.table-scroll`). This is the story
// that says whether it does, or whether the latency column just leaves.
export const ListPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: list([summary]),
};
