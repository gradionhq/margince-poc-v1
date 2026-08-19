// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ReactNode } from "react";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { AccountMenu, AccountRows } from "./account";
import { meFixture } from "./mefixture";

/**
 * Who is signed in, and the two things the product now offers nowhere else: the
 * door into settings, and the appearance choice.
 *
 * The block stands in TWO containers, which is what most of these frames are
 * here to show. In the top bar the trigger is an avatar at the top-right of the
 * viewport and the menu drops out of it, with the theme flyout opening to the
 * LEFT because there is no room on the right. In the sidebar the trigger is a
 * row at the foot of a tall column and the menu rises out of it, with the flyout
 * opening the other way. Same component, same rows; only the anchoring differs.
 *
 * The chip is the design system's `Avatar` rather than a mark of this block's
 * own: the monogram is taken the same way everywhere, and the tint is keyed on
 * the ADDRESS, so the reader carries one colour from the chrome to their own
 * account page and a rename does not move them to another one.
 *
 * fullscreen: the block measures itself against the container it sits in — the
 * collapsed rail is 64px, the expanded one 252px, the top bar's trail is flush
 * to the viewport's right edge — so every story renders the real chrome.
 * Storybook's default canvas padding would frame a geometry the product does not
 * use.
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
 * The block in the sidebar frame it sits in: the shell's grid gives the sidebar
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
 * The block in the top bar's trail, which is where the product renders it.
 *
 * The strip is the real one, so the trigger sits where it really sits: hard
 * against the viewport's right edge, with nothing to its right for a menu — or a
 * flyout — to open into. The frame is given height because the panel drops
 * DOWNWARD here, and a strip the height of its own row would show the menu
 * hanging off the bottom of a canvas that is not the page.
 */
function TopBarTrail({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div style={{ minHeight: "440px" }}>
      <header className="topbar">
        <div className="topbar-lead" />
        <div className="topbar-trail">{children}</div>
      </header>
    </div>
  );
}

/** Open the menu, the way the reader does. The panel's state is component-local
 *  (there is no `startOpen` prop), so the open frames drive the real trigger. */
async function openMenu({ canvasElement }: { canvasElement: HTMLElement }) {
  const canvas = within(canvasElement);
  await userEvent.click(canvas.getByRole("button", { name: /Account$/ }));
}

/** Open the menu and then the appearance flyout — two clicks, because that is
 *  what a reader spends to reach it. */
async function openThemeFlyout(context: { canvasElement: HTMLElement }) {
  await openMenu(context);
  const canvas = within(context.canvasElement);
  await userEvent.click(canvas.getByRole("menuitem", { name: "Theme" }));
}

/**
 * The top bar: the avatar alone, 32px, at the end of the trail.
 *
 * Nothing beside it is drawn — no name, no address, no chevron — so the mark is
 * the whole of the affordance, and the sentence it stands for reaches a screen
 * reader through the clipped line instead.
 */
export const TopBar: Story = {
  name: "Top bar trigger",
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <TopBarTrail>
          <AccountMenu variant="topbar" />
        </TopBarTrail>
      </StoryProviders>
    );
  },
};

/**
 * The panel, open, in the arrangement it ships in.
 *
 * Read top to bottom it is the whole of what this menu is for: who you are —
 * printed HERE because the trigger no longer prints it — the product's one door
 * into settings, the appearance choice, and the way out under its own rule.
 */
export const TopBarMenu: Story = {
  name: "Top bar menu",
  play: openMenu,
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <TopBarTrail>
          <AccountMenu variant="topbar" />
        </TopBarTrail>
      </StoryProviders>
    );
  },
};

/**
 * The appearance flyout, open, opening LEFT.
 *
 * That direction is the point of the frame: the menu is anchored to a trigger at
 * the viewport's right edge, so a flyout that opened outward would open past the
 * window. The tick is on the standing choice — it is a `menuitemradio`, and the
 * tick is the visible half of `aria-checked` rather than an ornament.
 */
export const TopBarThemeFlyout: Story = {
  name: "Top bar theme flyout",
  play: openThemeFlyout,
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <TopBarTrail>
          <AccountMenu variant="topbar" />
        </TopBarTrail>
      </StoryProviders>
    );
  },
};

/**
 * The same flyout in dark, because the surfaces it stacks are three token layers
 * deep — the page ground, the menu's elevated card, and the flyout's own card
 * over it — and a shadow that separates them in light can vanish in dark.
 */
export const TopBarThemeFlyoutDark: Story = {
  name: "Top bar theme flyout (dark)",
  globals: { theme: "dark" },
  play: openThemeFlyout,
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <TopBarTrail>
          <AccountMenu variant="topbar" />
        </TopBarTrail>
      </StoryProviders>
    );
  },
};

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
 * The rail's menu, which rises rather than drops — the block sits at the foot of
 * a column that fills the viewport, so there is no room under it — and whose
 * flyout therefore opens to the RIGHT. Same rows, mirrored geometry.
 */
export const ExpandedThemeFlyout: Story = {
  name: "Rail theme flyout",
  play: openThemeFlyout,
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
 * there is no trigger to open a popover from and nowhere for one to open into.
 * The rows stand on their own — identity, the settings door, the appearance
 * choice, the way out — and the appearance choice is FLAT here rather than a
 * flyout: three options visible at once is one tap, where a submenu would cost
 * a second tap to open and a third to pick, over a surface already the width of
 * the screen.
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
