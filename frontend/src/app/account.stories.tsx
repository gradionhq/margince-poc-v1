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
import { AccountMenu } from "./account";
import { meFixture } from "./mefixture";
// The strip's own sheet, loaded because THIS file builds a `.topbar` by hand and
// imports nothing that would pull it in. `.storybook/preview.tsx` loads the
// chrome sheets a story reaches by class rather than by module — but its list was
// written when `.topbar` still lived in shell.css, and the strip moved out into a
// sheet of its own with the shell restructure. Without this line every frame
// below drew an UNSTYLED strip: `display: block`, no height, no ground, no rule,
// the trail at the left edge and the menu anchored to the viewport instead of to
// the block. The frames still passed the render gate, because an unstyled
// element renders perfectly well.
import "./topbar.css";

/**
 * Who is signed in, and the two things the product now offers nowhere else: the
 * door into settings, and the appearance choice.
 *
 * ONE chrome: the avatar at the end of the strip's trail, with the menu dropping
 * out of it and the theme flyout opening LEFT because there is no room on the
 * right. The block had a sidebar form once — a row at the foot of the column,
 * menu rising, flyout mirrored — and it went with the foot it stood in; the
 * sidebar carries the installation's entitlement there now (Shell/Navigation
 * shell). Nothing here photographs an arrangement a reader cannot reach.
 *
 * The chip is the design system's `Avatar` rather than a mark of this block's
 * own: the monogram is taken the same way everywhere, and the tint is keyed on
 * the ADDRESS, so the reader carries one colour from the chrome to their own
 * account page and a rename does not move them to another one.
 *
 * fullscreen: the block measures itself against the container it sits in — the
 * strip's trail is flush to the content column's right edge, and the menu's drop
 * offset is computed from the strip's own height — so every story renders the
 * real chrome. Storybook's default canvas padding would frame a geometry the
 * product does not use.
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
 * The block in the top bar's trail, which is where the product renders it.
 *
 * The strip is the real one, INSIDE the real frame: `.app`, the sidebar taking
 * its column, the bar as the first row of `<main>`. That is not ceremony. Both
 * numbers the strip is built from — `--topbarH` and `--pageGutter` — are declared
 * on `.app` (app/shell.css), so a bar rendered on the bare canvas resolves them
 * to nothing: `height` and `padding` are then invalid declarations, the strip
 * collapses to its content, loses its gutter, and puts the trigger hard against
 * the WINDOW instead of against the content column's edge. The account menu's own
 * drop offset is computed from `--topbarH` too, so it lands wrong in the same
 * breath.
 *
 * All THREE of the bar's tracks are rendered, and the middle one is why: the
 * strip is `minmax(0, 1fr) auto minmax(0, 1fr)`, so a frame that supplied only a
 * lead and a trail put the trail in the MIDDLE track and left the third one
 * empty — the block then sat near the centre of the bar, which is the one thing
 * about its position these frames are for. The slot is the search's own, left
 * empty: what the trail needs from it is its width, and a live field here would
 * be a second component in a frame about this one. Below 1100px, where `fe-uat`
 * captures, the real slot is a 36px glyph and this one is 0px — so the block in
 * the captured frames sits about a glyph's width right of where it really does.
 */
function TopBarTrail({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className="app railexpanded">
      <div className="rail expanded" />
      <main className="main">
        <header className="topbar">
          <div className="topbar-lead" />
          <div className="topbar-searchslot" />
          <div className="topbar-trail">{children}</div>
        </header>
        <div className="scroll" />
      </main>
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
          <AccountMenu />
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
 *
 * Only ONE row carries a leading glyph, and it is Sign out. The other two are
 * words in a list of three, where a column of icons is decoration that has to be
 * scanned past; the way out is the row nobody wants to hit by accident, so it is
 * the one that earns a second signal beside its label. The chevron on Theme is
 * not that signal — it says there is a layer behind the row, which is a fact
 * about the control rather than an emblem for it.
 */
export const TopBarMenu: Story = {
  name: "Top bar menu",
  play: openMenu,
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <TopBarTrail>
          <AccountMenu />
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
          <AccountMenu />
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
          <AccountMenu />
        </TopBarTrail>
      </StoryProviders>
    );
  },
};
