// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ImportCard } from "./import";
import { installFetchStub, meRoute, StoryProviders } from "./story-utils";

// Bringing a customer's file in. The card is one of the two cleanest surfaces in
// settings — every state is a Callout, the wide table scrolls in its own box —
// and its own header calls the commit the least reversible write in the product.
//
// The flow past the first step needs a real file drop, which a story cannot
// perform: what is catalogued here is the resting state and the two answers the
// card gives before anybody chooses a file.
function story(allow: Parameters<typeof meRoute>[0]) {
  return () => {
    installFetchStub({ "GET /me": meRoute(allow) });
    return (
      <StoryProviders>
        <ImportCard />
      </StoryProviders>
    );
  };
}

const OPERATOR = { import_run: ["create", "read", "update"] } as const;

const meta: Meta<typeof ImportCard> = {
  title: "Settings/Organization/Maintenance/Import",
  component: ImportCard,
};
export default meta;
type Story = StoryObj<typeof ImportCard>;

export const ChooseAFile: Story = { render: story(OPERATOR) };

// Maintenance opens on the admin role OR an embedding-reindex read, so a seat
// holding only the latter reaches this page. It is told the import exists and is
// not theirs to run — an absent card would say the installation cannot import.
export const Withheld: Story = {
  render: story({ embedding_reindex: ["read"] }),
};
