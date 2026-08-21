// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { LinkedInReachCard } from "./linkedin-reach";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// LinkedInReachCard stories for the fe-uat render gate. The three cases that
// read differently are the three the card exists to keep apart: accounts the
// network reaches, a fresh workspace where nothing resolved yet (which still
// has to report the unresolved count), and a read that failed — which is NOT
// an empty network.
//
// Two of the three draw the SAME row: the report is the card's subject, so it
// lives in a stacked `SettingRow` whose description carries what the view cannot
// show, and an empty network is that row's `.empty` rather than a slab standing
// outside the list. Only a failed read replaces the list, because a read nobody
// managed to make is not a network that reaches nobody.

function reachStory(body: unknown, status = 200) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture()),
      "GET /me/linkedin-reach": () => jsonResponse(body, status),
    });
    return (
      <StoryProviders>
        <LinkedInReachCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof LinkedInReachCard> = {
  title: "Settings/You/Connections/LinkedIn reach",
  component: LinkedInReachCard,
};
export default meta;
type Story = StoryObj<typeof LinkedInReachCard>;

const REACHED = {
  accounts: [
    {
      organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
      display_name: "Nordwind Logistik GmbH",
      connections: 14,
      contacts_on_file: 3,
    },
    {
      organization_id: "018f3a1b-0000-7000-8000-0000000000a2",
      display_name: "Havelmann & Söhne",
      connections: 6,
      contacts_on_file: 6,
    },
  ],
  accounts_total: 9,
  unresolved_connections: 1420,
};

export const Reaches: Story = { render: reachStory(REACHED) };

// Nothing resolved yet, and the unresolved count matters MOST here: five
// thousand imported connections that matched no account is not "none yet". It is
// the row's DESCRIPTION, above the answer it qualifies, exactly where the
// truncation caveat sits when there are figures to truncate. What to check is
// that the whole row — label, caveat, sentence — costs about as much height as
// one row of the reach table, rather than the 90px slab `.empty` draws when it
// stands on a page of its own.
export const NothingResolvedYet: Story = {
  render: reachStory({
    accounts: [],
    accounts_total: 0,
    unresolved_connections: 5210,
  }),
};

// Nothing resolved and nothing unresolved either — a member who has recorded a
// profile and imported no export yet. There is no caveat to state, so the row
// draws no description at all rather than an empty one, and the sentence stands
// alone under the label.
export const NothingImportedYet: Story = {
  render: reachStory({
    accounts: [],
    accounts_total: 0,
    unresolved_connections: 0,
  }),
};

export const ReadFailed: Story = {
  render: reachStory(
    { title: "Internal Server Error", detail: "the reach index is rebuilding" },
    500,
  ),
};

// The table in dark. Two things here are token-driven and only prove it against
// the other palette: the hairline `SettingList` rules between its rows (there is
// one row today, so what shows is the absence of a trailing rule above the
// card's own edge), and the muted ink of the caveat line above the figures,
// which has to stay legibly below the label without disappearing into the card
// ground.
export const ReachesDark: Story = {
  globals: { theme: "dark" },
  render: reachStory(REACHED),
};

// The reach table at 390px. Every cell in it still holds its line
// (`.li-reach-cell`) — an account name, two figures — and the table is a real
// `<table>` inside DataTable's `.table-scroll` box, which is the whole of its
// narrow-screen answer: a German company name plus two counts is wider than a
// phone, so the table scrolls and the page does not. What to check is that the
// scroll really is the table's and not the page's — the stacked row wraps it in
// `.settingrow-measure`, which is what supplies the `min-width: 0` a scroll box
// needs inside a flex control column. The header row can no longer come apart
// from the figures it names — that was a `display: block` table's failure mode,
// and the scroll belongs to the wrapper rather than the table.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const ReachesPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: reachStory(REACHED),
};
