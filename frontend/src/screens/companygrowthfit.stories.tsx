// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { GrowthFitPanel } from "./companygrowthfit";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// What an account is worth to us. The band is never shown alone: a reader who
// sees "unknown" with nothing beside it cannot tell a poor fit from a thin
// record, so the completeness travels with it.
const STRONG = {
  organization_id: "o-brandt",
  band: "strong",
  band_capped_reason: null,
  data_completeness: { present: 4, expected: 4, missing: [] },
  next_step: "Ask the fleet manager who signs off the retrofit budget.",
};

// The case the band alone would misreport: the fit LOOKS weak because half the
// inputs are absent, and the reason says so rather than leaving the reader to
// infer it.
const CAPPED = {
  organization_id: "o-brandt",
  band: "unknown",
  band_capped_reason: "Not enough is known about this account to place it.",
  data_completeness: {
    present: 1,
    expected: 4,
    missing: ["company_size", "transformation_need", "access"],
  },
  next_step: null,
};

function story(fit: Record<string, unknown> | null) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ organization: ["read"] }),
      "GET /organizations/o-brandt/growth-fit": () =>
        fit ? jsonResponse(fit) : jsonResponse({ code: "not_found" }, 404),
    });
    return (
      <StoryProviders>
        <GrowthFitPanel orgId="o-brandt" enabled />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof GrowthFitPanel> = {
  title: "Records/Company/Growth fit",
  component: GrowthFitPanel,
};
export default meta;
type Story = StoryObj<typeof GrowthFitPanel>;

export const StrongFit: Story = { render: story(STRONG) };

export const CappedByMissingInputs: Story = { render: story(CAPPED) };

export const NotAssessed: Story = { render: story(null) };
