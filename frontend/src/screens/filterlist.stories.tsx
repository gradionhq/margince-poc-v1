// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { SaveFilterListAction } from "./filterlist";
import { newGroup, newLeaf } from "./segmentpredicate";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Turning a finished filter into a dynamic list — the shared, first-class kind of
// saved filter, as against the per-user view beside it.
//
// The dialog is NamePrompt's and documented there, so what these stories carry is
// this action's own two decisions: it is offered only for a filter the server
// would accept, and a refusal keeps the name on screen to be corrected.
const meta: Meta<typeof SaveFilterListAction> = {
  title: "Patterns/Save filter as list",
  component: SaveFilterListAction,
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

const COMPLETE = newGroup("and", [newLeaf("city", "eq", "Berlin")]);

type Story = StoryObj<typeof SaveFilterListAction>;

export const Offered: Story = {
  render: () => {
    installFetchStub({});
    return <SaveFilterListAction resource="person" tree={COMPLETE} />;
  },
};

export const WithheldForAnIncompleteFilter: Story = {
  // A clause with nothing typed. The server validates a definition on create, so
  // this would not be a list that reads oddly — it would be no list at all.
  // Deliberately an empty capture; that IS the behaviour.
  render: () => {
    installFetchStub({});
    return (
      <SaveFilterListAction
        resource="person"
        tree={newGroup("and", [newLeaf("city", "eq", "")])}
      />
    );
  },
};

export const NameAlreadyTaken: Story = {
  // The reason the server gave, with the name still in the box. Losing it on
  // failure would leave the reader retyping and unsure whether a list was made.
  render: () => {
    installFetchStub({
      "POST /lists": () =>
        jsonResponse(
          {
            title: "Duplicate name",
            status: 409,
            detail: "A list called Berliners already exists.",
          },
          409,
        ),
    });
    return <SaveFilterListAction resource="person" tree={COMPLETE} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const page = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Save as list" }),
    );
    await userEvent.type(await page.findByLabelText("List name"), "Berliners");
    await userEvent.click(page.getByRole("button", { name: "Create list" }));
    await page.findByRole("alert");
  },
};
