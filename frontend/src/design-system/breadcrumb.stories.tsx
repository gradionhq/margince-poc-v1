// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Building2 } from "lucide-react";
import { Breadcrumb } from "./breadcrumb";

/**
 * The trail that says where the reader is and how they got here.
 *
 * Its one caller in this product is the TOP BAR (app/topbar.tsx), not the page
 * head: the trail is chrome that says where the session is, and the page's own
 * name is a heading in the content column below it. The frames here render the
 * primitive on its own ground at `layout: "padded"`, because what they are about
 * is the rules rather than the strip — the strip itself is framed under
 * Shell/Top bar, where the trail can be seen shrinking against a real track edge.
 *
 * What they catch, in order: a single-stop trail is a real state and not a
 * degenerate one (TopLevelPage), the two-stop list→record shape is the one the
 * bar draws on a record route (ListToRecord), only the LAST stop gives way when
 * the row runs out of room while the ancestors keep their full width
 * (LongRecordName — read the ellipsis, then hover or tab to it for the whole
 * name), and a glyph rides with the label rather than replacing it (WithIcon).
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

/** The shape the top bar draws on a record route: the list, then the record. */
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
 *
 * The 420px box is the one hand-set width in this file, and it is here because
 * the pressure IS the story: the trail has to be given less room than it wants
 * before there is anything to look at, and the canvas at any reviewer's window
 * width would not do that reliably. In the product the same bound arrives as a
 * grid track rather than as a wrapper (`minmax(0, 1fr)` on the bar's lead), which
 * is the version Shell/Top bar frames.
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
