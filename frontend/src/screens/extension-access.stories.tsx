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
//
// Each registered object is one stacked `SettingRow` — a toggle matrix is the
// subject of its row, never an answer that fits beside the question — and what
// the unit BROUGHT (its objects, routes and jobs) reads last, behind a closed
// disclosure: it is reference an operator opens to check which object gates the
// route they care about, not a decision.
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
  seat: "full" | "read" = "full",
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({}, { roles, seat }),
      "GET /extensions": () => jsonResponse({ extensions }),
      // `roles`, which is what RoleDirectory names — not the `data` envelope the
      // paginated collections use. Keyed wrong, the read narrowed to an empty
      // list and every story in this file drew a matrix with no ROLE ROWS, over
      // the "nobody holds read" warning that an empty list makes vacuously true:
      // UnitsWithGrants and NothingGrantedYet were the same picture, and neither
      // was the matrix.
      "GET /roles": () =>
        jsonResponse({
          roles: ROLES.map((role) => ({ ...role, objects })),
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
  title: "Settings/Admin settings/People and access/Extensions and access",
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

// An admin on a read seat: every tick is legible and none of them is pressable,
// with the seat ceiling said once above the rows and attached to each switch as
// its own `reason`. Worth a story of its own because it is the state that is
// easiest to draw as an absent card, and an absent one would read as "this
// installation composes nothing".
export const ReadSeat: Story = {
  render: story([YOGI], ["admin"], { ext_yogi_briefing: READ }, "read"),
};

// The inventory and the matrix in dark. Three things here are drawn from tokens
// that mean "one step off the card ground", and dark is where a step that small
// either survives or collapses: `.ext-chip` fills an RBAC object and a route with
// --bgHover inside a card, the matrix separates every role row with a single
// --borderSubtle hairline, and the `SettingList` now rules between one object's
// grid and the next with the same hairline — two rules of the same weight, one
// inside a grid and one between two of them, which either read as a hierarchy or
// as a wall. The Switch tracks in the cells are the fourth — an off track and an
// on track have to stay two different things when the whole palette darkens
// under them.
export const UnitsWithGrantsDark: Story = {
  globals: { theme: "dark" },
  render: story([YOGI, DE], ["admin"], { ext_yogi_briefing: READ }),
};

// The matrix at 390px, where it is the widest thing in settings that is not a
// table of figures: a role column plus four CRUD columns, with both header rows
// and the role names deliberately nowrap. It is meant to scroll inside
// `.ext-matrix-wrap` rather than push the page sideways (the no-horizontal-page-
// scroll rule), and the scroller holds the checkboxes themselves so it stays
// keyboard-reachable. This is the width at which the stacked row's
// `.settingrow-measure` wrapper earns its place: without the `min-width: 0` it
// carries, the grid grows to its own width inside a flex control column and the
// PAGE scrolls instead of the table. Above it, the version Badge and the link to
// the unit's own page share the panel head with the unit name and are supposed
// to wrap onto their own row.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const UnitsWithGrantsPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([YOGI, DE], ["admin"], { ext_yogi_briefing: READ }),
};
