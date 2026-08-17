// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { GrowthFitPanel } from "./companygrowthfit";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// GrowthFitPanel answers what this company is worth to US, not what it is,
// and the band never renders alone (see the docblock in companygrowthfit.tsx):
// a reader who sees "unknown" needs the completeness figure and the next
// step beside it to tell "we could not judge" from "a poor fit". The stories
// below walk the bands from strong to the abstention floor, since each one
// changes what the panel is honestly allowed to say.

const meta: Meta = {
  title: "Screens/Company growth fit",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type GrowthFit = components["schemas"]["OrganizationGrowthFit"];

const assessmentSentence = (
  text: string,
): components["schemas"]["OrganizationBriefSentence"] => ({
  text,
  nature: "assessment",
  evidence: [
    {
      entity_type: "organization",
      entity_id: "o-1",
      name: "Brandt Automotive GmbH",
    },
  ],
});

// Every named dimension present, and every reason group non-empty: the
// ceiling case, so nothing here is standing in for a gap.
const strong: GrowthFit = {
  organization_id: "o-1",
  band: "strong",
  band_capped_reason: null,
  data_completeness: { present: 7, expected: 7, missing: [] },
  next_step: null,
  sub_scores: [
    {
      dimension: "industry_fit",
      score: 88,
      reason:
        "Fleet electrification is squarely inside the industries we sell into.",
    },
    {
      dimension: "company_size",
      score: 74,
      reason:
        "51-200 employees sits where our mid-market motion converts best.",
    },
    {
      dimension: "transformation_need",
      score: 91,
      reason: "Their own roadmap names the gap our platform closes.",
    },
    {
      dimension: "access",
      score: 65,
      reason: "One warm route in, through the Head of Fleet.",
    },
  ],
  positive_factors: [
    assessmentSentence(
      "Their fleet electrification pilot matches our core offering directly.",
    ),
  ],
  negative_factors: [
    assessmentSentence("Procurement has not yet been looped in."),
  ],
  whitespace: [
    assessmentSentence(
      "They have not adopted the analytics add-on we sell alongside this.",
    ),
  ],
  objections: [
    assessmentSentence(
      "Budget for this quarter is already committed elsewhere.",
    ),
  ],
  recommended_angle: assessmentSentence(
    "Lead with the electrification pilot renewal, then introduce the " +
      "add-on once that lands.",
  ),
  generated_at: "2026-07-10T09:00:00Z",
  generated_by: "model",
};

// The warn tone on the scale's other end, with fewer inputs available than
// the ceiling case: a reason to weigh the band down, not a reason to hide it.
const weak: GrowthFit = {
  organization_id: "o-1",
  band: "weak",
  band_capped_reason: null,
  data_completeness: {
    present: 5,
    expected: 7,
    missing: ["a confirmed decision-maker"],
  },
  next_step:
    "confirm who signs off on new vendors before investing further here",
  sub_scores: [
    {
      dimension: "industry_fit",
      score: 22,
      reason: "Their sector is adjacent to ours at best, not a direct match.",
    },
    {
      dimension: "company_size",
      score: 35,
      reason: "Below the headcount band where our offering has proven ROI.",
    },
    {
      dimension: "transformation_need",
      score: 18,
      reason: "Nothing on record names a gap our platform would close.",
    },
    {
      dimension: "access",
      score: 40,
      reason: "No confirmed route to a decision-maker yet.",
    },
  ],
  negative_factors: [
    assessmentSentence("No named decision-maker has engaged with us."),
  ],
  objections: [
    assessmentSentence(
      "They flagged budget as frozen for the rest of the year.",
    ),
  ],
  generated_at: "2026-07-10T09:00:00Z",
  generated_by: "model",
};

// `moderate` capped by our own offering context being unconfirmed (DOSS-AC-13):
// the capped reason and the next step both name the fix, and `missing` is
// partial rather than empty or complete.
const capped: GrowthFit = {
  organization_id: "o-1",
  band: "moderate",
  band_capped_reason:
    "our own offering context for this segment is not yet confirmed",
  data_completeness: {
    present: 4,
    expected: 7,
    missing: ["our own offering context", "any budget signal"],
  },
  next_step: "confirm our offering fit for this segment to lift the cap",
  sub_scores: [
    {
      dimension: "industry_fit",
      score: 60,
      reason: "Plausible fit, but the segment mapping is still provisional.",
    },
    {
      dimension: "access",
      score: 55,
      reason: "One contact has responded, no confirmed decision path yet.",
    },
  ],
  positive_factors: [
    assessmentSentence("They have engaged with two outreach attempts already."),
  ],
  generated_at: "2026-07-10T09:00:00Z",
  generated_by: "model",
};

// Below the abstention floor: no sub_scores at all (never zeroes, DOSS-AC-18),
// no reason groups, and `next_step` stands in for the score it is not making.
const unknown: GrowthFit = {
  organization_id: "o-1",
  band: "unknown",
  band_capped_reason: null,
  data_completeness: {
    present: 1,
    expected: 7,
    missing: ["their industry", "how big they are", "any recent activity"],
  },
  next_step: "log at least one meeting or call before this can be assessed",
  generated_at: "2026-07-10T09:00:00Z",
  generated_by: "deterministic",
};

function Panel({
  route,
}: Readonly<{ route: (body: unknown) => Response | Promise<Response> }>) {
  installFetchStub({ "GET /organizations/o-1/growth-fit": route });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 760 }}>
        <GrowthFitPanel orgId="o-1" enabled />
      </div>
    </StoryProviders>
  );
}

export const Strong: Story = {
  render: () => <Panel route={() => jsonResponse(strong)} />,
};

export const Weak: Story = {
  render: () => <Panel route={() => jsonResponse(weak)} />,
};

export const Capped: Story = {
  render: () => <Panel route={() => jsonResponse(capped)} />,
};

export const Abstention: Story = {
  render: () => <Panel route={() => jsonResponse(unknown)} />,
};

export const Loading: Story = {
  render: () => <Panel route={() => new Promise<Response>(() => undefined)} />,
};

// A payload this build cannot read renders identically whether the schema
// skewed underneath it or the request failed outright (companygrowthfit.tsx's
// `readable` guard treats both as "unavailable" rather than crashing on a
// half-shaped assembly), so one story stands in for both.
export const Unavailable: Story = {
  render: () => (
    <Panel
      route={() => jsonResponse({ organization_id: "o-1", band: "strong" })}
    />
  ),
};
