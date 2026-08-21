// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
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

// The organization's own agent identity, which bootstrap writes into every
// installation. It owns records, so it is in the roster — and it has no role at
// all, which is the one row whose answer is a sentence rather than a picker.
const AGENT = {
  id: "u-agent",
  email: "agent@brandt.gradion.local",
  display_name: "Brandt Agent",
  timezone: "Europe/Berlin",
  status: "active",
  is_agent: true,
  roles: [],
};

// Nine members is the roster the founder measured, and the number that decides
// whether this card reads as a list or as a wall: at one SettingRow per member
// it is nine lines, and at the old shape — a full-width role Select on its own
// line plus two stacked ghost buttons — it was nine 140px blocks.
const CROWD = [
  LARS,
  DANA,
  RETIRED,
  AGENT,
  ...["Mira Hoffmann", "Jonas Weber", "Ana Duarte", "Piet Klaassen"].map(
    (display_name, index) => ({
      id: `u-crowd-${index}`,
      email: `${display_name.split(" ")[0]?.toLowerCase()}@brandt.example`,
      display_name,
      timezone: "Europe/Berlin",
      status: "active",
      is_agent: false,
      roles: [index === 0 ? "manager" : "rep"],
    }),
  ),
  {
    id: "u-crowd-unset",
    email: "new@brandt.example",
    display_name: "Sofia Marchetti",
    timezone: "Europe/Berlin",
    status: "active",
    is_agent: false,
    // No role yet, so the picker reads its placeholder rather than an answer.
    roles: [],
  },
];

// `admin_password_link` is what decides whether a row's menu carries the
// set-password verb at all, and `meFixture` defaults it off — so a story about
// the row's verbs has to say so, or it shows a menu with one item.
function story(
  users: Record<string, unknown>[],
  identity: { roles?: string[]; seat?: "full" | "read" } = {},
  allow: GrantSpec = {},
  passwordLinks = false,
) {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          ...meFixture({ ...identity, allow }),
          admin_password_link: passwordLinks,
        }),
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
  title: "Settings/Admin settings/People and access/Members",
  component: UsersAdminCard,
};
export default meta;
type Story = StoryObj<typeof UsersAdminCard>;

export const Roster: Story = {
  render: story([LARS, DANA, RETIRED, AGENT], { roles: ["admin"] }),
};

// The case the redesign is for: the count and the invite verb share the header
// band, and nine members read as nine lines down one column of answers.
export const NineMembers: Story = {
  render: story(CROWD, { roles: ["admin"] }, {}, true),
};

// The header band is the tightest thing on this card at 720px — a title, a
// count and a verb on one line — and this is the render of that claim. The
// rows fall to one column under 640px (settingrow.css), which is what keeps a
// long name off three words per line.
export const RosterPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([LARS, DANA, RETIRED, AGENT], { roles: ["admin"] }, {}, true),
};

export const Empty: Story = { render: story([], { roles: ["admin"] }) };

// The roster answers "who is on my team", which is not an admin's private
// question — but administering it is. An operator seat without the admin role
// reads the card, gets each role as a fact rather than a picker, and is told in
// the card's own description that managing members is not theirs.
export const NotAnAdmin: Story = {
  render: story([LARS, DANA, RETIRED, AGENT], { roles: ["ops"] }),
};
