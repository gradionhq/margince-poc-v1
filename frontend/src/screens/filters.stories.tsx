// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { FiltersScreen } from "./filters";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The screen reads two routes and no session probe — the vocabulary for the
// object being filtered, and a preview once a clause is complete — so the stub
// only has to answer those two.
//
// The states worth capturing are the ones a reader can actually be in, and the
// difference between them is the judgement this screen carries: nothing asked
// yet (no count, no table), and asked (a count, and the rows behind it). A
// screenshot of the first is what proves the second is not the only state the
// surface knows how to draw.
const meta: Meta<typeof FiltersScreen> = {
  title: "Screens/Filters and views",
  component: FiltersScreen,
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

const PERSON_VOCAB = {
  resource: "person",
  fields: [
    {
      name: "full_name",
      type: "text",
      operators: ["eq", "neq", "in", "contains", "exists"],
      custom: false,
    },
    {
      name: "city",
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
  ],
};

const PREVIEW = {
  resource: "person",
  match_count: 3,
  columns: ["id", "full_name", "city", "cf_loyalty_tier", "created_at"],
  rows: [
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
      id: "p-3",
      full_name: "Chen Wei",
      city: "Singapore",
      cf_loyalty_tier: null,
      created_at: "2026-06-30",
    },
  ],
  truncated: false,
};

// One readable view and one this build cannot read, because the menu's rule is
// that the second kind is left out — a catalog page that only ever shows the
// happy row documents half the behaviour.
const SAVED_VIEWS = {
  data: [
    {
      id: "v-1",
      owner_id: "u-1",
      resource: "people",
      name: "Gold tier in Berlin",
      query: {
        filter: {
          and: [
            { field: "city", op: "eq", value: "Berlin" },
            { field: "cf_loyalty_tier", op: "eq", value: "gold" },
          ],
        },
      },
      version: 1,
    },
    {
      id: "v-2",
      owner_id: "u-1",
      resource: "people",
      name: "Saved by an older build",
      query: { filter: { and: [{ field: "city", op: "like", value: "Ber" }] } },
      version: 1,
    },
  ],
  page: { next_cursor: null, has_more: false },
};

function routes(): void {
  installFetchStub({
    "GET /filters/vocabulary": () => jsonResponse(PERSON_VOCAB),
    "POST /filters/preview": () => jsonResponse(PREVIEW),
    "GET /views": () => jsonResponse(SAVED_VIEWS),
  });
}

type Story = StoryObj<typeof FiltersScreen>;

export const NothingAskedYet: Story = {
  // The state the screen opens in. The count says so in words rather than
  // showing a zero, and there is no results table at all — an empty one would
  // report that nothing matches a filter nobody has written.
  render: () => {
    routes();
    return <FiltersScreen />;
  },
};

export const LoadedFromASavedView: Story = {
  // The whole round trip: a stored tree becomes an editable one, and the count
  // comes back for it without a clause being retyped. The menu holds two rows
  // and offers one — the unreadable view is not on it.
  render: () => {
    routes();
    return <FiltersScreen />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // The menu's panel is anchored to its trigger from OUTSIDE the canvas — a
    // card clips its own overflow, so the panel cannot live inside one. Its items
    // are therefore found through the document, not the story's root.
    const page = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Load a saved filter" }),
    );
    await userEvent.click(
      await page.findByRole("button", { name: "Gold tier in Berlin" }),
    );
    await canvas.findByText("3 contacts match");
  },
};

export const WithAClause: Story = {
  // The builder owns its own tree, so the only way to reach the answered state
  // is to author a clause the way a human does. fe-uat waits for a play()
  // interaction to settle before it screenshots.
  render: () => {
    routes();
    return <FiltersScreen />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // Waited for, not assumed: the builder is a skeleton until the vocabulary
    // answers, and there is no Add clause button to click before then.
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add clause" }),
    );
    await userEvent.type(canvas.getByLabelText("Value"), "Berlin");
    await canvas.findByText("3 contacts match");
  },
};
