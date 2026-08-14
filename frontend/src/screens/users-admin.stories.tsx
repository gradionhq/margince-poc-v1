// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";
import { UsersAdminCard } from "./users-admin";

const LARS = {
  id: "u-1",
  email: "lars@brandt.example",
  display_name: "Lars Brandt",
  timezone: "Europe/Berlin",
  status: "active",
  is_agent: false,
  roles: ["admin"],
};

const DANA = {
  id: "u-2",
  email: "dana@brandt.example",
  display_name: "Dana Kessler",
  timezone: "Europe/Berlin",
  status: "active",
  is_agent: false,
  roles: ["rep"],
};

// A deactivated seat still occupies the roster: the card lists everyone with a
// place in the installation, which is the question it answers.
const RETIRED = {
  id: "u-3",
  email: "otto@brandt.example",
  display_name: "Otto Fischer",
  timezone: "Europe/Berlin",
  status: "deactivated",
  is_agent: false,
  roles: ["rep"],
};

function story(users: Record<string, unknown>[], roles: string[]) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({}, { roles }),
      "GET /users": () => jsonResponse({ data: users }),
    });
    return (
      <StoryProviders>
        <UsersAdminCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof UsersAdminCard> = {
  title: "Settings/Organization/People and access/Members",
  component: UsersAdminCard,
};
export default meta;
type Story = StoryObj<typeof UsersAdminCard>;

export const Roster: Story = {
  render: story([LARS, DANA, RETIRED], ["admin"]),
};

export const Empty: Story = { render: story([], ["admin"]) };

// The roster answers "who is on my team", which is not an admin's private
// question — but administering it is. A rep reads the card and is told so,
// rather than finding a page that looks like the installation has no members.
export const NotAnAdmin: Story = {
  render: story([LARS, DANA], ["rep"]),
};
