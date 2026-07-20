import type { Meta, StoryObj } from "@storybook/react-vite";
import { AttainmentRing } from "../design-system/atoms";
import { LocaleProvider } from "../i18n";
import { QuotasView } from "./quotas";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Quotas & attainment stories for the fe-uat render gate: the honest data
// states (attainment banded met/accent/behind, empty, and the two 422
// refusals) render off the same fetch-stub shapes the unit tests exercise —
// never a live call.

const page = { next_cursor: null, has_more: false };

const ownerQuota = {
  id: "q1",
  workspace_id: "w1",
  owner_id: "u1",
  team_id: null,
  period_start: "2026-07-01",
  period_end: "2026-09-30",
  target_minor: 28000000,
  currency: "EUR",
  version: 3,
  created_at: "2026-06-28T16:40:00Z",
  updated_at: "2026-07-01T09:12:00Z",
};

const users = {
  data: [
    {
      id: "u1",
      workspace_id: "w1",
      email: "riya@example.co",
      display_name: "Riya Patel",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
  ],
  page,
};

const baseAttainment = {
  quota_id: "q1",
  target_minor: 28000000,
  currency: "EUR",
  as_of_date: "2026-08-15",
  pace_pct: 64,
  contributing_deals: [
    { deal_id: "d1", base_value_minor: 17707200 },
    { deal_id: "d2", base_value_minor: 9450000 },
  ],
};

function attainment(band: "met" | "accent" | "behind") {
  if (band === "met") {
    return {
      ...baseAttainment,
      closed_won_minor: 31387200,
      attainment_pct: 113,
      gap_minor: 3387200,
      band,
    };
  }
  if (band === "accent") {
    return {
      ...baseAttainment,
      closed_won_minor: 20160000,
      attainment_pct: 72,
      gap_minor: -7840000,
      band,
    };
  }
  return {
    ...baseAttainment,
    closed_won_minor: 11480000,
    attainment_pct: 41,
    gap_minor: -16520000,
    band,
  };
}

function quotaStory(routes: Record<string, () => Response>) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <QuotasView />
      </StoryProviders>
    );
  };
}

const withAttainment = (band: "met" | "accent" | "behind") =>
  quotaStory({
    "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
    "GET /quotas/q1/attainment": () => jsonResponse(attainment(band)),
    "GET /users": () => jsonResponse(users),
    "GET /deals/d1": () =>
      jsonResponse({ id: "d1", name: "BÄR Pharma — Packaging QA" }),
    "GET /deals/d2": () =>
      jsonResponse({ id: "d2", name: "Brandt — Line QA Retrofit" }),
  });

const meta: Meta<typeof QuotasView> = {
  title: "screens/quotas",
  component: QuotasView,
};
export default meta;
type Story = StoryObj<typeof QuotasView>;

export const AttainmentMet: Story = { render: withAttainment("met") };
export const AttainmentAccent: Story = { render: withAttainment("accent") };
export const AttainmentBehind: Story = { render: withAttainment("behind") };

export const NoQuota: Story = {
  render: quotaStory({
    "GET /quotas": () => jsonResponse({ data: [], page }),
  }),
};

export const TargetZero: Story = {
  render: quotaStory({
    "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
    "GET /users": () => jsonResponse(users),
    "GET /quotas/q1/attainment": () =>
      jsonResponse(
        {
          code: "attainment_target_zero",
          detail: "target is zero — set a target to compute attainment",
          status: 422,
        },
        422,
      ),
  }),
};

export const ComputeError: Story = {
  render: quotaStory({
    "GET /quotas": () => jsonResponse({ data: [ownerQuota], page }),
    "GET /users": () => jsonResponse(users),
    "GET /quotas/q1/attainment": () =>
      jsonResponse(
        {
          code: "attainment_computation_failed",
          detail: "the clean-core query timed out",
          status: 422,
        },
        422,
      ),
  }),
};

// The ring atom on its own, banded — the pure visual the attainment card
// composes (no fetch, no providers beyond the locale).
export const Ring: StoryObj<typeof AttainmentRing> = {
  render: () => (
    <LocaleProvider initial="en">
      <div style={{ display: "flex", gap: "var(--space-6)" }}>
        <AttainmentRing pct={113} band="met" caption="attained" />
        <AttainmentRing pct={72} band="accent" caption="attained" />
        <AttainmentRing pct={41} band="behind" caption="attained" />
      </div>
    </LocaleProvider>
  ),
};
