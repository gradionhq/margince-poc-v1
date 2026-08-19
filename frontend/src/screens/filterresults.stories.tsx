// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { FilterPreview, VocabularyField } from "./filterdata";
import { FilterResults } from "./filterresults";
import { StoryProviders } from "./story-utils";

// FilterResults takes everything as props — the preview response, the vocabulary
// it heads its columns from — so it needs no fetch and no query client of its
// own. StoryProviders is here for the locale, which every string on it reads
// through.
//
// The three stories are the three things a reader can be looking at: rows behind
// a count, a filter that selected nothing, and the recount that has not answered
// yet. The middle one matters most, because "nothing matched" and "something
// failed" look identical in a table that only knows how to be empty.
const meta: Meta<typeof FilterResults> = {
  title: "Screens/Filter results",
  component: FilterResults,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <StoryProviders>
        <Story />
      </StoryProviders>
    ),
  ],
};
export default meta;

const FIELDS: readonly VocabularyField[] = [
  {
    name: "full_name",
    type: "text",
    operators: ["eq", "neq", "in", "contains", "exists"],
    custom: false,
  },
  {
    name: "cf_loyalty_tier",
    type: "picklist",
    operators: ["eq", "neq", "in", "exists"],
    custom: true,
  },
];

const COLUMNS = ["id", "full_name", "city", "cf_loyalty_tier", "created_at"];

/** A page of rows, one of them missing the value the filter asked about. */
const ROWS = [
  {
    id: "p-1",
    full_name: "Ann Lee",
    city: "Berlin",
    cf_loyalty_tier: "gold",
    created_at: "2026-08-01",
  },
  {
    id: "p-2",
    full_name: "Bruno Sá",
    city: "Lisbon",
    cf_loyalty_tier: "silver",
    created_at: "2026-07-19",
  },
  {
    // The dash case, which is the reason a null does not render as blank: a
    // reader checking an `exists` clause has to be able to see the absence.
    id: "p-3",
    full_name: "Chen Wei",
    city: "Singapore",
    cf_loyalty_tier: null,
    created_at: "2026-06-30",
  },
] satisfies FilterPreview["rows"];

function preview(rows: FilterPreview["rows"]): FilterPreview {
  return {
    resource: "person",
    match_count: rows.length,
    columns: COLUMNS,
    rows,
    truncated: false,
  };
}

type Story = StoryObj<typeof FilterResults>;

const shared = {
  fields: FIELDS,
  // The filter asked about tier, so tier is a column — the derivation this
  // component exists to make, rather than a fixed product column list.
  named: ["cf_loyalty_tier"],
  unit: "contacts",
  widthsKey: "story-filter-preview",
  pending: false,
};

export const Rows: Story = {
  args: { ...shared, preview: preview(ROWS) },
};

export const NothingMatched: Story = {
  args: { ...shared, preview: preview([]) },
};

export const Recounting: Story = {
  // The state between an edit and its answer. The rows on screen are the PREVIOUS
  // filter's, which is why the screen marks the count stale rather than blanking
  // it — a table that empties on every keystroke is unreadable.
  args: { ...shared, preview: preview(ROWS), pending: true },
};
