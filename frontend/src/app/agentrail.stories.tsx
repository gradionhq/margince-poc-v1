// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useMutation } from "@tanstack/react-query";
import { type ReactNode, useEffect } from "react";
import { userEvent, within } from "storybook/test";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { AgentRail } from "./agentrail";

// The section's states come from what the installation ANSWERS, so a story is
// a set of answers rather than a set of props. Each one below is a posture an
// installation is genuinely in — a deployment with no model bound, a mailbox
// whose token expired, an unlicensed workspace — because a story for a state
// no server can produce documents a screen nobody will see.
//
// It is mounted the way the shell mounts it: inside a rail's own foot
// (app/shell.tsx), because its geometry is the rail's own, to the pixel, and a
// story that floated it on blank canvas would be reviewing a different
// layout from the one that ships.

type Answers = Readonly<{
  aiState: "configured" | "unconfigured" | "development";
  connectorStatus: "connected" | "reauth_required";
  licenseState: "valid" | "absent" | "rejected";
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

/** A write held open for the life of the story — the only way to reach
 *  `working` without a real mutation somewhere else in the tree to trigger. */
function PendingWrite() {
  const write = useMutation({
    mutationFn: () => new Promise<void>(() => {}),
  });
  // biome-ignore lint/correctness/useExhaustiveDependencies: mutate is stable for the life of this hook; listing it would refire the pending write on every render instead of holding one open.
  useEffect(() => {
    write.mutate();
  }, []);
  return null;
}

function Rail({
  collapsed,
  children,
}: Readonly<{ collapsed?: boolean; children: ReactNode }>) {
  return (
    <nav className={collapsed ? "rail collapsed" : "rail expanded"}>
      <div className="grow" />
      <div className="railfoot">{children}</div>
    </nav>
  );
}

function story(answers: Answers, collapsed = false) {
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
      "GET /installation/license": () =>
        jsonResponse({
          state: answers.licenseState,
          seats_used: 1,
          over_limit: false,
          checked_at: "2026-08-01T09:00:00Z",
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
      "GET /ai/usage": () =>
        jsonResponse({
          days: [],
          budget: { monthly_tokens: 0, spent_tokens: 0, band: "normal" },
        }),
    });
    return (
      <StoryProviders>
        <Rail collapsed={collapsed}>
          <AgentRail route={{ screen: "companies" }} />
        </Rail>
      </StoryProviders>
    );
  };
}

const HEALTHY: Answers = {
  aiState: "configured",
  connectorStatus: "connected",
  licenseState: "valid",
  approvals: 0,
  duplicates: 0,
  calls: [
    { task: "growth_fit", minutesAgo: 12 },
    { task: "summarize", minutesAgo: 47 },
    { task: "brief_ranking", minutesAgo: 190 },
  ],
};

const meta: Meta<typeof AgentRail> = {
  title: "Shell/Agent rail",
  component: AgentRail,
};
export default meta;
type Story = StoryObj<typeof AgentRail>;

/** Idle: every source reachable, nothing waiting, a model bound, a valid licence. */
export const Idle: Story = { render: story(HEALTHY) };

/** Ingest: a read of the record on screen is in flight. Held open by a
 *  connectors read the fetch stub never answers, the same evidence the real
 *  section derives `ingest` from. */
export const Ingest: Story = {
  render: () => {
    installFetchStub({
      "GET /assistant/profile": () =>
        jsonResponse({
          name: "Margince",
          kind: "ai",
          state: "configured",
          inference_mode: "cloud",
          providers: ["anthropic"],
        }),
      "GET /installation/license": () =>
        jsonResponse({
          state: "valid",
          seats_used: 1,
          over_limit: false,
          checked_at: "2026-08-01T09:00:00Z",
        }),
      "GET /connectors": () => new Promise<Response>(() => {}),
      "GET /ai/usage": () =>
        jsonResponse({
          days: [],
          budget: { monthly_tokens: 0, spent_tokens: 0, band: "normal" },
        }),
    });
    return (
      <StoryProviders>
        <Rail>
          <AgentRail route={{ screen: "companies" }} />
        </Rail>
      </StoryProviders>
    );
  },
};

/** Working: a write in flight. `PendingWrite` holds one mutation open for the
 *  life of the story, the same evidence the real section derives `working`
 *  from. */
export const Working: Story = {
  render: () => {
    installFetchStub({
      "GET /assistant/profile": () =>
        jsonResponse({
          name: "Margince",
          kind: "ai",
          state: "configured",
          inference_mode: "cloud",
          providers: ["anthropic"],
        }),
      "GET /installation/license": () =>
        jsonResponse({
          state: "valid",
          seats_used: 1,
          over_limit: false,
          checked_at: "2026-08-01T09:00:00Z",
        }),
      "GET /ai/usage": () =>
        jsonResponse({
          days: [],
          budget: { monthly_tokens: 0, spent_tokens: 0, band: "normal" },
        }),
    });
    return (
      <StoryProviders>
        <PendingWrite />
        <Rail>
          <AgentRail route={{ screen: "companies" }} />
        </Rail>
      </StoryProviders>
    );
  },
};

/** Warning: no licence bound. The orb goes amber and the line names the fault
 *  rather than raising it as a hard failure. */
export const Warning: Story = {
  render: story({ ...HEALTHY, licenseState: "absent" }),
};

/** Error: a mailbox the agent cannot reach — the token expired and capture is
 *  paused. Red means not connected, and the line says what is not connected. */
export const SourceUnreachable: Story = {
  render: story({ ...HEALTHY, connectorStatus: "reauth_required" }),
};

/** Error: no model bound at all — the one posture where nothing else on the
 *  section matters. */
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

/** The collapsed rail: the orb is the whole report at 64px, the words and the
 *  chevron gone. */
export const CollapsedRail: Story = {
  render: story(HEALTHY, true),
};

/** The panel open, showing the recap, the runtime row and the workspace
 *  section together — the detail the block itself has no room for. */
export const PanelOpen: Story = {
  render: story({ ...HEALTHY, approvals: 3, duplicates: 2 }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /expand/i }),
    );
  },
};
