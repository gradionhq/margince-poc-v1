// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { ListQuery } from "./listquery";
import {
  LoadFilterViewMenu,
  SaveFilterViewAction,
  SaveViewAction,
} from "./savedviews";
import { newGroup, newLeaf } from "./segmentpredicate";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// A saved view is per-user list or filter state, by name. This module has three
// visible surfaces and they are documented together because what they share is
// the thing worth seeing: ONE naming dialog, so a list and the segment builder
// ask the same question the same way.
//
// The offering rules are the other half. Both save actions render NOTHING until
// there is something worth saving — an unnarrowed list, or an incomplete filter,
// gets no button — so the stories that show the withheld state carry as much as
// the ones that show the button.
const meta: Meta = {
  title: "Patterns/Saved views",
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

const VIEWS = {
  data: [
    {
      id: "v-1",
      owner_id: "u-1",
      resource: "people",
      name: "Gold tier in Berlin",
      query: {
        filter: { and: [{ field: "city", op: "eq", value: "Berlin" }] },
      },
      version: 1,
    },
    {
      // Not offered: `like` is not an operator this engine has, so the stored
      // tree cannot be read, and an entry that restores nothing is worse than no
      // entry at all.
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
  installFetchStub({ "GET /views": () => jsonResponse(VIEWS) });
}

const NARROWED: ListQuery = {
  q: "ann",
  sort: "-created_at",
  includeArchived: false,
  filters: { owner: "me" },
  perPage: 25,
};

const UNNARROWED: ListQuery = {
  q: "",
  sort: "",
  includeArchived: false,
  filters: {},
  perPage: 25,
};

type Story = StoryObj;

export const NamingAView: Story = {
  // The one dialog both surfaces share, opened.
  render: () => {
    routes();
    return <SaveViewAction resource="people" query={NARROWED} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Save view" }));
  },
};

export const NothingWorthSaving: Story = {
  // An unnarrowed list offers no save: the view would do what the All tab
  // already does, and a rail of those is how a useful feature becomes clutter.
  // Deliberately an empty capture — that IS the documented behaviour.
  render: () => {
    routes();
    return <SaveViewAction resource="people" query={UNNARROWED} />;
  },
};

export const SavingAFilter: Story = {
  // The segment builder's side of the same dialog. Offered because the tree is
  // complete; an incomplete one is refused by the engine, so saving it would
  // store a view that fails the moment anybody opens it.
  render: () => {
    routes();
    return (
      <SaveFilterViewAction
        resource="people"
        tree={newGroup("and", [newLeaf("city", "eq", "Berlin")])}
      />
    );
  },
};

export const NoSaveForAnIncompleteFilter: Story = {
  // A clause with nothing typed in it. Also an empty capture, for the same
  // reason as NothingWorthSaving.
  render: () => {
    routes();
    return (
      <SaveFilterViewAction
        resource="people"
        tree={newGroup("and", [newLeaf("city", "eq", "")])}
      />
    );
  },
};

export const TheRailFailedToLoad: Story = {
  // The one place that says the saved-view rail did not load, and it says WHICH
  // surface: the notice lands beside a list's Columns and Compact buttons, where
  // an unnamed "this section did not load" could be any of the three.
  render: () => {
    installFetchStub({
      "GET /views": () => jsonResponse({ title: "Server error" }, 500),
    });
    return <SaveViewAction resource="people" query={NARROWED} />;
  },
};

export const LoadingASavedFilter: Story = {
  // Two stored views, one offered: the menu leaves out what it cannot read.
  render: () => {
    routes();
    return <LoadFilterViewMenu resource="people" onLoad={() => undefined} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Load a saved filter" }),
    );
  },
};
