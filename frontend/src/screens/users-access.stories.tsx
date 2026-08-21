// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";
import { TeamsCard } from "./users-access";

// Who may edit whose records, now that every seat reads every customer record.
// The card had no story at all, which is how its create form sat as the last row
// of a list of teams — labelled "New team" beside a button reading "Create team"
// — without anybody seeing the shape.

const TEAMS = [
  { id: "t-1", name: "DACH Sales", member_count: 4 },
  // The singular. This row read "1 members" until the card borrowed the
  // roster's own count copy.
  { id: "t-2", name: "Benelux", member_count: 1 },
  // A team nobody is on yet edits nothing but its members' own records, so the
  // zero is worth reading rather than hiding.
  { id: "t-3", name: "Enterprise (forming)", member_count: 0 },
];

function story(teams: Record<string, unknown>[]) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({}),
      // A page, the way the endpoint answers one: the card reads the shared
      // roster walk, which follows `page.next_cursor` to the end of the list.
      "GET /teams": () =>
        jsonResponse({
          data: teams,
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <TeamsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof TeamsCard> = {
  title: "Settings/Admin settings/People and access/Teams",
  component: TeamsCard,
};
export default meta;
type Story = StoryObj<typeof TeamsCard>;

export const Teams: Story = { render: story(TEAMS) };

// Nothing to list is the state the create verb matters most in, and it is the
// state that used to draw a bare "No teams yet." line where every other card on
// the tab draws an empty state.
export const NoTeams: Story = { render: story([]) };

// The header band carries a title and a verb on one line; a team's name and its
// count fall to one column under 640px.
export const TeamsPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story(TEAMS),
};
