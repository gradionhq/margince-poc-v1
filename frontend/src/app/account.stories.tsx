// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { AccountMenu, AccountRows } from "./account";
import { meFixture } from "./mefixture";

/**
 * Who is signed in, at the foot of the sidebar.
 *
 * The chip is the design system's `Avatar` rather than a mark of this block's
 * own, which is what the states below are here to show: the monogram is taken
 * the same way everywhere, and the tint is keyed on the ADDRESS, so the reader
 * carries one colour from the rail to their own account page and a rename does
 * not move them to another one.
 *
 * fullscreen: the block measures itself against the sidebar it sits in — the
 * collapsed rail is 64px and the expanded one 252px — so every story renders
 * the real `.app` grid. Storybook's default canvas padding would frame a
 * geometry the product does not use.
 */
const meta: Meta<typeof AccountMenu> = {
  title: "Shell/Account block",
  component: AccountMenu,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof AccountMenu>;

type SessionUser = components["schemas"]["MeResponse"]["user"];

/**
 * The session the block prints, routed explicitly.
 *
 * `GET /me` is the one route a story may not leave to the stub's fallback: an
 * unrouted probe reads as a malformed session, the block falls to its
 * unresolved branch, and the story's name is then the only thing still claiming
 * what it shows.
 */
function stubSession(user: Partial<SessionUser> = {}) {
  const me = meFixture();
  installFetchStub({
    "GET /me": () => jsonResponse({ ...me, user: { ...me.user, ...user } }),
  });
}

/**
 * The probe that has not answered yet — left unresolved on purpose, because
 * "in flight" is the state under review and any answer would end it before the
 * catalog could show it.
 */
function stubUnresolvedSession() {
  installFetchStub({ "GET /me": () => new Promise<Response>(() => {}) });
}

/**
 * The block in the frame it really sits in: the shell's grid gives the sidebar
 * its width, and the foot is the band under the navigation. The content column
 * is present and empty on purpose — the sidebar is flush to the frame's left
 * edge and reads as an edge only against something beside it.
 */
function RailFoot({
  collapsed = false,
  children,
}: Readonly<{ collapsed?: boolean; children: ReactNode }>) {
  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <div className={collapsed ? "rail collapsed" : "rail expanded"}>
        <div className="grow" />
        <div className="railfoot">{children}</div>
      </div>
      <main className="main" />
    </div>
  );
}

/**
 * The labeled rail: the chip, the name over the address, and the chevron that
 * says there is a menu behind the row. Two initials, because the session
 * carries a display name of two words.
 */
export const Expanded: Story = {
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <RailFoot>
          <AccountMenu collapsed={false} />
        </RailFoot>
      </StoryProviders>
    );
  },
};

/**
 * 64px, where the chip is the whole row.
 *
 * Nothing else is rendered — the name, the address and the chevron are gone,
 * and the sentence they carry reaches a screen reader through the clipped line
 * instead. So the mark is the only thing left identifying the reader, which is
 * why it has to be the same mark their account page draws.
 */
export const Collapsed: Story = {
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <RailFoot collapsed>
          <AccountMenu collapsed />
        </RailFoot>
      </StoryProviders>
    );
  },
};

/**
 * A session with no display name, which is the case the monogram's address
 * split exists for.
 *
 * The address becomes the label — nobody is given a name the product made up —
 * and it is not repeated on a second line under itself. The monogram then comes
 * off the address's own parts rather than off a name that is not there, so this
 * reader still gets two letters instead of one initial or an empty circle.
 */
export const AddressOnly: Story = {
  name: "Address as the label",
  render: () => {
    stubSession({ display_name: "" });
    return (
      <StoryProviders>
        <RailFoot>
          <AccountMenu collapsed={false} />
        </RailFoot>
      </StoryProviders>
    );
  },
};

/**
 * Before the session resolves.
 *
 * The chip keeps its box so the row does not jump when the name arrives, and it
 * shows a person glyph — never initials of a name nobody has, and never an
 * empty circle that reads as a mark that failed to load.
 */
export const SessionUnresolved: Story = {
  name: "Session not resolved",
  render: () => {
    stubUnresolvedSession();
    return (
      <StoryProviders>
        <RailFoot>
          <AccountMenu collapsed={false} />
        </RailFoot>
      </StoryProviders>
    );
  },
};

/**
 * The phone sheet's flat form.
 *
 * At this width the rail is a bottom bar and "More" expands it into a sheet, so
 * there is no trigger to open a popover from and nowhere above the foot for one
 * to open into. The rows stand on their own, and they carry the identity line
 * with them: the sheet is the phone's whole sidebar, and without that line
 * nothing on it says whose account it is offering.
 *
 * The sheet's layout is a viewport media query, so this story is honest only at
 * phone width — `uat-phone` is what makes the capture gate render it there, and
 * a reviewer reads it in Storybook with the viewport tool or by narrowing the
 * browser.
 */
export const PhoneSheet: Story = {
  name: "Phone sheet rows",
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <div className="app railexpanded">
          <div className="rail expanded sheetopen">
            <div className="railfoot">
              <AccountRows />
            </div>
          </div>
          <main className="main" />
        </div>
      </StoryProviders>
    );
  },
};
