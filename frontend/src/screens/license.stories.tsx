// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { LicenseReading } from "./license";
import { StoryProviders } from "./story-utils";

// Settings → License. The reading is rendered directly rather than through the
// fetching card: every state worth looking at is a state of the ENTITLEMENT, and
// stubbing a query to reach it would put the fetch on trial instead of the
// surface.

type Entitlement = components["schemas"]["LicenseEntitlement"];

const CHECKED_AT = "2026-08-15T09:00:00Z";

function story(entitlement: Entitlement) {
  return () => (
    <StoryProviders>
      <LicenseReading entitlement={entitlement} />
    </StoryProviders>
  );
}

const meta: Meta<typeof LicenseReading> = {
  title: "Settings/Organization/License",
  component: LicenseReading,
};
export default meta;
type Story = StoryObj<typeof LicenseReading>;

// Room left, which is what most installations look like most of the time.
export const InsideTheGrant: Story = {
  render: story({
    state: "valid",
    seats_used: 9,
    seats_granted: 10,
    over_limit: false,
    checked_at: CHECKED_AT,
  }),
};

// The state this screen exists for. Three things are on trial together: the
// interrupting notice, the alert tint on the slot whose figure caused it, and a
// meter whose value is past its own maximum — the bar fills and stops, and the
// numbers beside it are what say by how much.
export const OverTheGrant: Story = {
  render: story({
    state: "valid",
    seats_used: 11,
    seats_granted: 10,
    over_limit: true,
    checked_at: CHECKED_AT,
  }),
};

// A license that caps nothing: a count with no limit to read it against, so the
// granted slot carries a word and there is no meter at all. The story exists
// because the tempting render — a bar against zero — invents a limit nobody set.
export const NoSeatLimit: Story = {
  render: story({
    state: "valid",
    seats_used: 40,
    over_limit: false,
    checked_at: CHECKED_AT,
  }),
};

// No license configured, which is a supported state that runs: every development
// and CI installation is in it. It must not read as an installation that is out
// of seats.
export const Unlicensed: Story = {
  render: story({
    state: "absent",
    seats_used: 12,
    over_limit: false,
    checked_at: CHECKED_AT,
  }),
};

// Over the grant in dark, because this is the surface with the most colour on it
// and every derived value is a color-mix that follows the dark accent lift: the
// danger callout, the alert-tinted slot beside a plain one, and the meter's fill
// are three different tints that have to stay apart from each other AND from the
// card under them.
export const OverTheGrantDark: Story = {
  globals: { theme: "dark" },
  render: story({
    state: "valid",
    seats_used: 11,
    seats_granted: 10,
    over_limit: true,
    checked_at: CHECKED_AT,
  }),
};

// At 390px. The strip is two slots, so what narrow tests is whether they still
// read as ONE comparison once they fold — used above granted rather than beside
// it is a different sentence, and the meter under both is what has to keep them
// tied together.
export const OverTheGrantNarrow: Story = {
  globals: { viewport: { value: "phone" } },
  render: story({
    state: "valid",
    seats_used: 11,
    seats_granted: 10,
    over_limit: true,
    checked_at: CHECKED_AT,
  }),
};
