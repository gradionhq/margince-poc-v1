// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ExportFilterMenu } from "./filterexport";
import { newGroup, newLeaf } from "./segmentpredicate";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Exporting what a filter selects. Two labelled buttons rather than a menu,
// because there are exactly two formats and hiding a two-item list behind a
// click costs a reader more than it saves — and the header this sits under
// already spends its one unlabelled "…" on the saved-view rail.
//
// A success hands the browser a file and leaves the page looking exactly as it
// did, so there is nothing to screenshot in it. The states worth capturing are
// the two where something is visibly different: the export withheld, and the
// export refused.
const meta: Meta<typeof ExportFilterMenu> = {
  title: "Patterns/Filter export",
  component: ExportFilterMenu,
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

type Story = StoryObj<typeof ExportFilterMenu>;

export const Offered: Story = {
  render: () => {
    installFetchStub({});
    return <ExportFilterMenu resource="person" tree={COMPLETE} />;
  },
};

export const WithheldForAnIncompleteFilter: Story = {
  // A clause with nothing typed in it is refused per-leaf by the engine, so an
  // export button here would answer 422 and tell the reader nothing they could
  // not have been spared. Deliberately an empty capture — that IS the behaviour.
  render: () => {
    installFetchStub({});
    return (
      <ExportFilterMenu
        resource="person"
        tree={newGroup("and", [newLeaf("city", "eq", "")])}
      />
    );
  },
};

export const Refused: Story = {
  // The server's own reason, beside the button that failed. Not "request
  // failed": a bulk read can be refused for something a reader can act on, and
  // the refusal has to land somewhere or somebody waits for a file that is
  // never coming.
  render: () => {
    installFetchStub({
      "POST /exports": () =>
        jsonResponse(
          {
            title: "Export refused",
            status: 403,
            detail: "Bulk record read is human-only.",
          },
          403,
        ),
    });
    return <ExportFilterMenu resource="person" tree={COMPLETE} />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Export CSV" }));
    await canvas.findByRole("alert");
  },
};
