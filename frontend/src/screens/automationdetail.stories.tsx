// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import {
  AutomationPreview,
  AutomationRuns,
  OutcomeBadge,
} from "./automationdetail";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The two human-only automation panels rendered against static fixtures — the
// same fetch-stub convention the unit tests use, so every visual state below
// (each outcome, empty, error, loading; preview normal / not-computable /
// hidden / loading / error) is exercised without a live stack.

type AutomationRun = components["schemas"]["AutomationRun"];

const meta: Meta = {
  title: "Screens/AutomationDetail",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const run = (over: Partial<AutomationRun>): AutomationRun => ({
  id: "r1",
  automation_id: "au-1",
  occurred_at: "2026-07-14T10:00:00Z",
  outcome: "fired",
  tier: "auto_execute",
  ...over,
});

const RUNS_PATH = "GET /automations/au-1/runs";
const PREVIEW_PATH = "POST /automations/au-1/preview";

// One run per outcome, so the list doubles as the visual legend for the
// badge tone+glyph map across all five first-class outcomes.
const mixedRuns: AutomationRun[] = [
  run({
    id: "r-fired",
    outcome: "fired",
    tier: "auto_execute",
    trigger_evidence: "deal BÄR Pharma entered Negotiation",
    target_ref: "deal:BÄR Pharma",
    action_result: "created follow-up task",
  }),
  run({
    id: "r-failed",
    outcome: "failed",
    tier: "confirmation_required",
    trigger_evidence: "no activity 14d on deal Globex Renewal",
    target_ref: "deal:Globex Renewal",
    reason: "email provider error",
  }),
  run({
    id: "r-blocked",
    outcome: "blocked",
    tier: "confirmation_required",
    target_ref: "person:Anna Weber",
    reason: "Passport no longer permits send",
  }),
  run({
    id: "r-skipped",
    outcome: "skipped",
    tier: "auto_execute",
    reason: "already had an open task",
  }),
  run({
    id: "r-queued",
    outcome: "queued_for_approval",
    tier: "confirmation_required",
    approval_required: true,
    action_result: "staged to approval inbox",
  }),
];

export const OutcomeBadges: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ display: "flex", gap: "var(--space-2)", flexWrap: "wrap" }}>
        <OutcomeBadge outcome="fired" />
        <OutcomeBadge outcome="failed" />
        <OutcomeBadge outcome="blocked" />
        <OutcomeBadge outcome="skipped" />
        <OutcomeBadge outcome="queued_for_approval" />
      </div>
    </StoryProviders>
  ),
};

export const RunsMixed: Story = {
  render: () => {
    installFetchStub({
      [RUNS_PATH]: () =>
        jsonResponse({ data: mixedRuns, page: { next_cursor: null } }),
    });
    return (
      <StoryProviders>
        <AutomationRuns automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const RunsEmpty: Story = {
  render: () => {
    installFetchStub({
      [RUNS_PATH]: () =>
        jsonResponse({ data: [], page: { next_cursor: null } }),
    });
    return (
      <StoryProviders>
        <AutomationRuns automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const RunsError: Story = {
  render: () => {
    installFetchStub({
      [RUNS_PATH]: () => jsonResponse({ detail: "automation not found" }, 404),
    });
    return (
      <StoryProviders>
        <AutomationRuns automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const RunsLoading: Story = {
  render: () => {
    // A never-settling response holds the panel in its loading skeleton.
    installFetchStub({
      [RUNS_PATH]: () => new Promise<Response>(() => {}),
    });
    return (
      <StoryProviders>
        <AutomationRuns automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const PreviewNormal: Story = {
  render: () => {
    installFetchStub({
      [PREVIEW_PATH]: (body) =>
        jsonResponse({
          matches_now: 12,
          would_have_fired: 34,
          window_days: (body as { window_days: number }).window_days,
          excluded_by_permission: 2,
        }),
    });
    return (
      <StoryProviders>
        <AutomationPreview automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const PreviewNotComputable: Story = {
  render: () => {
    installFetchStub({
      [PREVIEW_PATH]: (body) =>
        jsonResponse({
          matches_now: 5,
          would_have_fired: null,
          window_days: (body as { window_days: number }).window_days,
        }),
    });
    return (
      <StoryProviders>
        <AutomationPreview automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const PreviewLoading: Story = {
  render: () => {
    installFetchStub({
      [PREVIEW_PATH]: () => new Promise<Response>(() => {}),
    });
    return (
      <StoryProviders>
        <AutomationPreview automationId="au-1" />
      </StoryProviders>
    );
  },
};

export const PreviewError: Story = {
  render: () => {
    installFetchStub({
      [PREVIEW_PATH]: () =>
        jsonResponse({ detail: "window_days must be 1..90" }, 422),
    });
    return (
      <StoryProviders>
        <AutomationPreview automationId="au-1" />
      </StoryProviders>
    );
  },
};
