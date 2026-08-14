// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ExtensionAccessCard } from "./extension-access";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The role × CRUD matrix: what each composed unit brought into the installation
// and which roles may reach it. Until somebody grants one, an enabled unit
// renders "you do not hold access" for every seat — which is why this surface
// exists and why its withheld state is worth looking at.
const YOGI = {
  name: "yogi",
  version: "0.4.1",
  rbac_objects: ["ext_yogi_briefing"],
  routes: [{ path: "/ext/yogi/brief", method: "GET" }],
  jobs: ["yogi_nightly_brief"],
};

const DE = {
  name: "de",
  version: "1.2.0",
  rbac_objects: [],
  routes: [],
  jobs: [],
};

const ROLES = [
  { key: "admin", name: "Admin", is_system: true, version: 3 },
  { key: "rep", name: "Rep", is_system: true, version: 3 },
];

const NONE = { create: false, read: false, update: false, delete: false };
const READ = { ...NONE, read: true };

function story(
  extensions: Record<string, unknown>[],
  roles: string[],
  objects: Record<string, unknown> = {},
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({}, { roles }),
      "GET /extensions": () => jsonResponse({ extensions }),
      "GET /roles": () =>
        jsonResponse({
          data: ROLES.map((role) => ({ ...role, objects })),
        }),
    });
    return (
      <StoryProviders>
        <ExtensionAccessCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ExtensionAccessCard> = {
  title: "Settings/Organization/People and access/Extensions and access",
  component: ExtensionAccessCard,
};
export default meta;
type Story = StoryObj<typeof ExtensionAccessCard>;

export const UnitsWithGrants: Story = {
  render: story([YOGI, DE], ["admin"], { ext_yogi_briefing: READ }),
};

// The state a fresh installation is actually in: the unit is enabled, its object
// is registered, and no role has been pointed at it yet.
export const NothingGrantedYet: Story = {
  render: story([YOGI, DE], ["admin"], { ext_yogi_briefing: NONE }),
};

export const NoUnitsComposed: Story = { render: story([], ["admin"]) };

// A rep reads the inventory and cannot change who reaches it. The matrix stays
// on screen — an absent one would say this installation composes nothing.
export const NotAnAdmin: Story = {
  render: story([YOGI], ["rep"], { ext_yogi_briefing: READ }),
};
