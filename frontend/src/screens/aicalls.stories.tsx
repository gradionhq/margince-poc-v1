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
  title: "Settings/Organization/AI/Model calls",
  component: AiCallsCard,
};
export default meta;
type Story = StoryObj<typeof AiCallsCard>;
export const List: Story = { render: list([summary]) };
export const Empty: Story = { render: list([]) };

// No automation grant: the trace keeps its place and says it is withheld. An
// absent card would read as "this installation made no model calls".
export const Withheld: Story = { render: list([summary], true, {}) };

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

// The detail panel IN the table, which is the only place a reader meets it:
// the disclosure button opens the attempt trail under its own row, so the
// trace stays readable as one thing rather than two surfaces side by side.
export const RowExpanded: Story = {
  render: list([summary]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const disclosure = await canvas.findByRole("button", {
      name: /Show the attempt trail for capture_classify/,
    });
    await userEvent.click(disclosure);
    await canvas.findByText("Attempts");
  },
};
