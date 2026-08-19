// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Building2 } from "lucide-react";
import { Breadcrumb } from "./breadcrumb";

/**
 * The trail that says where the reader is and how they got here.
 *
 * What these stories are for, in order of what they actually catch: a
 * single-stop trail is a real state and not a degenerate one (TopLevelPage),
 * the two-stop list→record shape is the one the page head draws (ListToRecord),
 * only the LAST stop gives way when the row runs out of room while the
 * ancestors keep their full width (LongRecordName — narrow the canvas or read
 * the ellipsis, then hover or tab to it for the whole name), and a glyph rides
 * with the label rather than replacing it (WithIcon).
 *
 * Flip the Theme toolbar to check both renderings; every colour is a token.
 */
const meta: Meta<typeof Breadcrumb> = {
  title: "Design System/Breadcrumb",
  component: Breadcrumb,
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj<typeof Breadcrumb>;

// The landmark name arrives translated, like all copy in this tier.
const LANDMARK = "Breadcrumb";

/** One stop: a top-level page that leads back to nothing. */
export const TopLevelPage: Story = {
  args: {
    label: LANDMARK,
    items: [{ label: "Companies" }],
  },
};

/** The shape the page head draws on a record route: the list, then the record. */
export const ListToRecord: Story = {
  args: {
    label: LANDMARK,
    items: [
      { label: "Companies", href: "#/companies" },
      { label: "Brandt Logistik GmbH" },
    ],
  },
};

/**
 * The case the primitive exists for. The two ancestors are short labels the
 * product chose; the record's name is user data of unbounded length, so it is
 * the half that truncates and keeps the trail on one line.
 */
export const LongRecordName: Story = {
  args: {
    label: LANDMARK,
    items: [
      { label: "Deals", href: "#/deals" },
      { label: "Brandt Logistik GmbH", href: "#/companies/c-1" },
      {
        label:
          "Rahmenvertrag Kontraktlogistik Nordwest — Verlängerung 2026/2027 inklusive Zusatzflächen Bremerhaven",
        lang: "de",
      },
    ],
  },
  render: (args) => (
    <div style={{ maxWidth: 420 }}>
      <Breadcrumb {...args} />
    </div>
  ),
};

/** A glyph leads the stop it belongs to; the label is still the whole name. */
export const WithIcon: Story = {
  args: {
    label: LANDMARK,
    items: [
      { label: "Companies", href: "#/companies", icon: Building2 },
      { label: "Sontana Werke AG" },
    ],
  },
};
