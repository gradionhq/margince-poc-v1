// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ThinState } from "./person360";
import "./person360.css";

/**
 * The thin-record surface: what a person page says when it has almost nothing
 * to say about the person.
 *
 * It had no story, which is how its one control shipped carrying a bare
 * `className="btn"`. A bare `btn` names no variant, and the variants are what
 * carry the fill, the border and the ink — so the single move this surface
 * offers rendered transparent and borderless on the card behind it. That is
 * invisible to a type checker and to every test that only asks whether the
 * button is in the document.
 */
const meta: Meta<typeof ThinState> = {
  title: "Records/Person record/Thin record",
  component: ThinState,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj<typeof ThinState>;

type View = components["schemas"]["Person360"];
const page = { has_more: false, next_cursor: null };

// A record carrying a name, an address and nothing else — which is the state
// this surface exists for. The employments list is empty on purpose: the
// remediation the card offers is chosen by what is MISSING, and an absent
// employer is what sends it down the other branch.
const bare: View = {
  as_of: "2026-08-13T09:00:00Z",
  person: {
    id: "p-9",
    full_name: "Mara Vogel",
    first_name: "Mara",
    last_name: "Vogel",
    owner_id: "u-1",
    emails: [
      {
        id: "pe-9",
        person_id: "p-9",
        email: "mara.vogel@example.test",
        email_type: "work",
        is_primary: true,
        position: 0,
        source: "manual",
      },
    ],
  },
  employments: { data: [], page },
  activities: { data: [], page },
  deals: { data: [], page },
  colleagues: { data: [], page },
} as unknown as View;

// The same record, with an employer on it. The card offers a different move,
// which is the whole point of the branch — a menu of two would say the surface
// does not know which one matters.
const withEmployer: View = {
  ...bare,
  employments: {
    data: [
      {
        id: "em-9",
        person_id: "p-9",
        organization_id: "o-9",
        organization_name: "Brandt Logistik",
        is_current_primary: true,
      },
    ],
    page,
  },
} as unknown as View;

/** No employer: the move on offer is to name where this person works. */
export const NoEmployer: Story = {
  render: () => <ThinState view={bare} onLogActivity={() => {}} />,
};

/** An employer, so the move on offer is to capture something that happened. */
export const WithEmployer: Story = {
  render: () => <ThinState view={withEmployer} onLogActivity={() => {}} />,
};

/**
 * No move at all. The caller passes no handler, so the card states the
 * situation and offers nothing — which has to read as a deliberate absence
 * rather than as a button that failed to draw. That distinction is exactly
 * what the unstyled button destroyed.
 */
export const NothingToOffer: Story = {
  render: () => <ThinState view={bare} />,
};

/**
 * Dark, because the card's plate, the sentence on it and the primary button are
 * three surfaces whose separation is carried by tokens that move between
 * themes.
 */
export const NoEmployerDark: Story = {
  name: "No employer — dark",
  globals: { theme: "dark" },
  render: () => <ThinState view={bare} onLogActivity={() => {}} />,
};
