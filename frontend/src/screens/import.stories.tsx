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

// The resting state in dark, and the reason it is dark rather than narrow: this
// card's own sheet opens by declaring that every quiet line on it reads --textMeta
// and not --textMuted, because --textMuted measures 1.54:1 here while --textMeta
// is the canonical AA small-text role — a rule written against the LIGHT palette
// and, until this story, never looked at once both tokens re-resolved. The lines
// under test are the object hint and the file-format sentence, sitting beside a
// SegmentedControl whose selected segment is the loudest thing on the card.
//
// A narrow variant would prove less: the flow past the first step needs a real
// file drop, so the wide mapping table and its .import__scroll box — the parts
// that have a width problem to have — are not reachable from a story at all.
export const ChooseAFileDark: Story = {
  globals: { theme: "dark" },
  render: story(OPERATOR),
};
