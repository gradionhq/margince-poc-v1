// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { AgentTaskbar } from "./agenttaskbar";
import { AttentionProvider } from "./attention";

// The taskbar's states come from what the installation ANSWERS, so a story is a
// set of answers rather than a set of props. Each one below is a posture an
// installation is genuinely in — a deployment with no model bound, a mailbox
// whose token expired, a queue waiting on a person — because a story for a state
// no server can produce documents a screen nobody will see.

type Answers = Readonly<{
  aiState: "configured" | "unconfigured" | "development";
  connectorStatus: "connected" | "reauth_required";
  approvals: number;
  duplicates: number;
  calls: readonly Readonly<{ task: string; minutesAgo: number }>[];
}>;

const NOW = Date.parse("2026-08-19T10:00:00Z");

function callRow(task: string, minutesAgo: number, index: number) {
  return {
    id: `call-${index}`,
    occurred_at: new Date(NOW - minutesAgo * 60_000).toISOString(),
    task,
    tier: "cheap_cloud",
    provider: "anthropic",
    model_id: "claude-sonnet-4-6",
    served_model: "claude-sonnet-4-6",
    calls_attempted: 1,
    tokens_in: 900,
    tokens_out: 120,
    reasoning_tokens: 0,
    cached_tokens: 0,
    latency_ms: 840,
    has_payload: false,
  };
}

function story(answers: Answers) {
  return () => {
    installFetchStub({
      "GET /assistant/profile": () =>
        jsonResponse({
          name: "Margince",
          kind: "ai",
          state: answers.aiState,
          inference_mode:
            answers.aiState === "development" ? "development" : "cloud",
          providers: answers.aiState === "configured" ? ["anthropic"] : [],
        }),
      "GET /approvals": () =>
        jsonResponse({
          data: Array.from({ length: answers.approvals }, (_, index) => ({
            id: `approval-${index}`,
            status: "pending",
          })),
        }),
      "GET /connectors": () =>
        jsonResponse({
          data: [
            {
              provider: "gmail",
              status: answers.connectorStatus,
              account_label: "ada@acme.test",
            },
          ],
        }),
      "GET /dedupe/candidates": () =>
        jsonResponse({
          data: Array.from({ length: answers.duplicates }, (_, index) => ({
            id: `dupe-${index}`,
            status: "open",
          })),
        }),
      "GET /ai/calls": () =>
        jsonResponse({
          data: answers.calls.map((call, index) =>
            callRow(call.task, call.minutesAgo, index),
          ),
          tasks: [],
        }),
      "GET /agent-tools": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <AttentionProvider>
          <AgentTaskbar route={{ screen: "companies" }} />
        </AttentionProvider>
      </StoryProviders>
    );
  };
}

const HEALTHY: Answers = {
  aiState: "configured",
  connectorStatus: "connected",
  approvals: 0,
  duplicates: 0,
  calls: [
    { task: "growth_fit", minutesAgo: 12 },
    { task: "summarize", minutesAgo: 47 },
    { task: "brief_ranking", minutesAgo: 190 },
  ],
};

const meta: Meta<typeof AgentTaskbar> = {
  title: "Shell/Agent taskbar",
  component: AgentTaskbar,
};
export default meta;
type Story = StoryObj<typeof AgentTaskbar>;

/** Rest: every source reachable, nothing waiting, a model bound. */
export const Idle: Story = { render: story(HEALTHY) };

/** Proposals waiting on a person — a count beside the orb, not a state of its own. */
export const ApprovalsWaiting: Story = {
  render: story({ ...HEALTHY, approvals: 12 }),
};

/** Duplicate pairs the agent will not decide for itself. */
export const DuplicatesOpen: Story = {
  render: story({ ...HEALTHY, duplicates: 3 }),
};

/** A mailbox the agent cannot reach: the token expired and capture is paused. */
export const SourceUnreachable: Story = {
  render: story({ ...HEALTHY, connectorStatus: "reauth_required" }),
};

/** No model bound at all — the one posture where nothing else on the bar matters. */
export const NoModelConfigured: Story = {
  render: story({ ...HEALTHY, aiState: "unconfigured" }),
};

/** The development path: it answers, and every answer it gives is invented. */
export const DevelopmentModel: Story = {
  render: story({ ...HEALTHY, aiState: "development" }),
};

/** A fresh installation: a model is bound and nothing has run through it yet. */
export const NothingHasRunYet: Story = {
  render: story({ ...HEALTHY, calls: [] }),
};
