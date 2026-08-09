// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Building2, Globe, Link2, MapPin, Users } from "lucide-react";
import { LocaleProvider } from "../i18n";
import { Chip, Meter, Sparkline } from "./readings";

// The three reading primitives: a proportion, a series, an attribute.
const meta: Meta = {
  title: "Design System/Readings",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <div style={{ maxWidth: 480, display: "grid", gap: "var(--space-5)" }}>
          <Story />
        </div>
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

// Value and max are the two halves of one fact, so the bar and the label
// beside it are drawn from the same pair and cannot disagree.
export const Meters: Story = {
  render: () => (
    <>
      <div>
        <p className="t-caption">7 of 9 inputs present</p>
        <Meter value={7} max={9} label="Dossier completeness" />
      </div>
      <div>
        <p className="t-caption">Payment behaviour — low is the bad end</p>
        <Meter value={3} max={10} label="Payment behaviour" tone="warn" />
      </div>
      <div>
        <p className="t-caption">Nothing measured yet</p>
        <Meter value={0} max={0} label="Coverage" />
      </div>
    </>
  ),
};

export const Sparklines: Story = {
  render: () => (
    <>
      <Sparkline
        points={[12, 9, 14, 11, 18, 7, 12]}
        label="Days paid after due, last six months"
      />
      <Sparkline points={[8, 8, 8, 8]} label="Unchanged over four months" />
    </>
  ),
};

export const Chips: Story = {
  render: () => (
    <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>
      <Chip icon={Globe} href="https://glazedfrog.example">
        glazedfrog.example
      </Chip>
      <Chip icon={Link2} href="https://www.linkedin.com/company/example">
        LinkedIn
      </Chip>
      <Chip icon={MapPin}>London, UK</Chip>
      <Chip icon={Building2}>Building products</Chip>
      <Chip icon={Users}>51–200 employees</Chip>
    </div>
  ),
};
