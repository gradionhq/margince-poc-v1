// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import {
  Building2,
  Database,
  KeyRound,
  Mail,
  Mic,
  Plug,
  ShieldCheck,
  Sparkles,
  UserRound,
  UsersRound,
  Wrench,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { Card } from "../design-system/atoms";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
  StoryProviders,
} from "../screens/story-utils";
import type { GrantSpec } from "./mefixture";
import type { NavSection } from "./nav";
import { CommandPalette, useBuiltinCommands } from "./palette";
import type { Route } from "./router";
import { PageTitle, Shell, WorkspaceRail } from "./shell";
import { TopBar } from "./topbar";

// fullscreen: the shell sizes itself to the viewport, so Storybook's default
// canvas padding would clip the sidebar foot and misrepresent the layout — and
// the foot is where the entitlement row reports the installation's seats. The
// account block is NOT there any more; it stands at the end of the top bar's
// trail (Shell/Account block), which is why every frame here renders that bar.
const meta: Meta<typeof Shell> = {
  title: "Shell/Navigation shell",
  component: Shell,
  parameters: {
    layout: "fullscreen",
    // The `phone` viewport this file's last story selects is declared once for
    // the whole catalog in .storybook/preview.tsx — it stopped being this
    // file's private need the moment the settings pages wanted it too.
  },
};
export default meta;
type Story = StoryObj<typeof Shell>;

// What is waiting, spelled once and handed to BOTH halves of the chrome the way
// the shell hands it: the Approvals row's badge in the panel and the bell's chip
// in the strip are two readings of one queue, and a frame that fed one and left
// the other at nothing would draw the same queue as 12 and as empty at once.
const COUNTS = { inbox: 12, tasks: 4 };

/**
 * The session every frame here mounts on, plus whatever routes the frame is about.
 *
 * `allow` is the grants, not a role: three things in this chrome are capability
 * questions — the entitlement row at the foot asks `license:read`, and the two app
 * banners ask `automation:update` and `embedding_reindex:read` — so a hand-written
 * body carrying no `authorization` at all answers every one of them closed. That
 * looks exactly like a deliberate denial on screen, which is how a frame comes to
 * be named for something it never draws. `meRoute({})` is the honest default here:
 * an admin on a full seat holding no object grants.
 */
function stubSession(allow: GrantSpec = {}, about: RouteMap = {}) {
  installFetchStub({
    "GET /me": meRoute(allow),
    "GET /ai/usage": () =>
      jsonResponse({
        days: [],
        budget: { monthly_tokens: 100, spent_tokens: 20, band: "normal" },
      }),
    ...about,
  });
}

// The brand block reads the installation profile from the cache the onboarding
// gate fills in the real app; a story seeds the same entry so the organization
// line renders. Without it the block honestly shows the product name alone.
function SeedInstallation({ children }: Readonly<{ children: ReactNode }>) {
  const client = useQueryClient();
  if (client.getQueryData(["company"]) === undefined) {
    client.setQueryData(["company"], {
      organization_id: "org-1",
      display_name: "Gradion GmbH",
    });
  }
  return <>{children}</>;
}

/**
 * The search row wired to the thing it actually opens.
 *
 * `onOpenSearch` is not a decorative prop: the top bar's field is the only
 * pointer route into the command palette, so a story that stubbed it would show
 * a control that does nothing and prove nothing. The app mounts the palette
 * beside the shell (App.tsx) — so does this, off the same builtin command list.
 */
function usePaletteSeam() {
  const [open, setOpen] = useState(false);
  const commands = useBuiltinCommands();
  const palette = (
    <CommandPalette
      open={open}
      onClose={() => setOpen(false)}
      commands={commands}
    />
  );
  return { openSearch: () => setOpen(true), palette };
}

function ShellExample({ children }: Readonly<{ children: ReactNode }>) {
  const { openSearch, palette } = usePaletteSeam();
  return (
    <>
      <Shell onOpenSearch={openSearch} counts={COUNTS}>
        {children}
      </Shell>
      {palette}
    </>
  );
}

export const Default: Story = {
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <SeedInstallation>
          <ShellExample>
            <div className="wrap">
              <Card as="div">Content</Card>
            </div>
          </ShellExample>
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

/**
 * One sidebar state, in the frame it really sits in.
 *
 * The `.app` grid is what gives the sidebar its width (64px collapsed, 252px
 * labeled) and the content column its edge, so the example renders the grid
 * rather than a hand-set width — otherwise the story would be showing geometry
 * the product does not use. The content column is present and empty on purpose:
 * the sidebar is flush to the frame's left edge, separated by its own
 * border-right and nothing else, and that reads only against something beside it.
 *
 * The collapse control is live, so each panel can be moved to the other state —
 * the two are one component, and a story where the control does nothing hides
 * the transition it exists for.
 */
function SidebarExample({
  initiallyCollapsed,
}: Readonly<{ initiallyCollapsed: boolean }>) {
  const [collapsed, setCollapsed] = useState(initiallyCollapsed);
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <WorkspaceRail
        route={{ screen: "deals" }}
        counts={COUNTS}
        collapsed={collapsed}
      />
      <main className="main">
        <TopBar
          route={{ screen: "deals" }}
          counts={COUNTS}
          collapsed={collapsed}
          onToggle={() => setCollapsed((current) => !current)}
          onOpenSearch={openSearch}
        />
        <div className="scroll" />
      </main>
      {palette}
    </div>
  );
}

// Both sidebar states, side by side. Expanded is 252px of 34px rows on a 4px
// gutter; collapsed is the canonical 64px geometry, where a row IS its target and
// keeps 44px, the logomark stands alone in the head, and the group headings go
// transparent and draw a 22px hairline inside the box they kept — so nothing
// below them re-spaces across the collapse.
//
// What is NOT in either panel: the search and the account block. Both moved to
// the strip above (app/topbar.tsx), which is why these frames render that strip
// rather than the sidebar alone — and the current row's own signal is now a tint
// plus a weight plus the accent on its glyph, with the indicator bar that used to
// sit in the gutter gone.
export const SidebarStates: Story = {
  name: "sidebar — expanded and collapsed",
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <SeedInstallation>
          <div style={{ display: "flex", height: "100vh" }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <SidebarExample initiallyCollapsed={false} />
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <SidebarExample initiallyCollapsed />
            </div>
          </div>
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

/**
 * The entitlement the foot reports, in the shapes it reports it in.
 *
 * Four postures, and the copy says which: a cap to count against, a license that
 * caps nothing, no license at all, and one the installation refused. Only the
 * first two are worth a frame — "No license" and "License refused" are the same
 * geometry as the third with different words in it, and a picture of the same row
 * twice is noise. What differs and therefore earns a frame is the INK: `pressing`
 * turns the row to the warning token when there is something to act on rather
 * than merely know.
 *
 * `checked_at` is a fixed instant rather than `new Date()`: a fixture that reads
 * the clock makes a capture that differs from the last one for no reason, and the
 * row never prints it anyway.
 */
type LicenseEntitlement = components["schemas"]["LicenseEntitlement"];

const CHECKED_AT = "2026-08-19T09:00:00Z";

const WITHIN_CAP: LicenseEntitlement = {
  state: "valid",
  seats_used: 12,
  seats_granted: 25,
  over_limit: false,
  checked_at: CHECKED_AT,
};

const OVER_CAP: LicenseEntitlement = {
  state: "valid",
  seats_used: 27,
  seats_granted: 25,
  over_limit: true,
  checked_at: CHECKED_AT,
};

/**
 * The session a frame about the foot needs: `license:read`, and the entitlement
 * itself.
 *
 * Both halves, because either one alone renders NOTHING and the two absences look
 * identical on screen — the row is gone for a principal who may not read it and
 * gone again while the read is in flight.
 */
function stubEntitlement(entitlement: LicenseEntitlement) {
  stubSession(
    { license: ["read"] },
    { "GET /installation/license": () => jsonResponse(entitlement) },
  );
}

/**
 * Seats used against seats granted, at the foot of both panels.
 *
 * The foot is where a tool puts what the installation is entitled to, and seats
 * are the one number about it that changes under people while they work: an
 * invitation spends one, and the refusal when the last is gone arrives in the
 * middle of adding a colleague.
 *
 * Rendered in both states side by side because the row has two geometries and
 * only one of them is a reading. Expanded it takes the destinations' own metrics
 * to the pixel — 34px, the same 9px icon-to-label gap, the same 14px inset — and
 * says it is a different KIND of row in its ink rather than by standing on a
 * second grid. Collapsed it keeps 44px like every other row at that width, drops
 * to the glyph, and carries the reading in a tooltip on hover and on keyboard
 * focus.
 *
 * ONE half of that is not what this frame photographs, and the reason is worth
 * more than the frame. The collapsed panel's rows are 44px, so its intrinsic
 * height is 774px — and `.rail.collapsed` sets `overflow: visible` so the
 * collapsed tooltips can escape its 64px box, which means it neither scrolls nor
 * clips. In a window shorter than that the foot is simply BELOW the bottom of the
 * viewport with nothing to scroll, and `fe-uat` captures at 720px. So the right
 * half of this frame shows the destinations and no foot at all. The expanded
 * panel is unaffected (34px rows, and it does scroll); read the collapsed foot in
 * Storybook in a window taller than 775px, and treat the shortfall as a note
 * against the panel rather than against the frame.
 */
export const RailEntitlement: Story = {
  name: "the foot — seats used against granted",
  render: () => {
    stubEntitlement(WITHIN_CAP);
    return (
      <StoryProviders>
        <SeedInstallation>
          <div style={{ display: "flex", height: "100vh" }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <SidebarExample initiallyCollapsed={false} />
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <SidebarExample initiallyCollapsed />
            </div>
          </div>
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

/**
 * Over the cap, which is the state the row changes colour for.
 *
 * Reported, never enforced: the installation keeps working, nothing on this row
 * blocks anybody, and what to DO about it is on the settings tab the row leads
 * to. The warning ink is the whole of the escalation the chrome is allowed — the
 * same treatment a license in grace or due for renewal gets, because all four are
 * one question ("does somebody need to act on this?") and a chrome row is the
 * wrong place to spell out which.
 */
export const RailEntitlementPressing: Story = {
  name: "the foot — over the seat cap",
  render: () => {
    stubEntitlement(OVER_CAP);
    return (
      <StoryProviders>
        <SeedInstallation>
          <SidebarExample initiallyCollapsed={false} />
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

/**
 * A principal without `license:read`, where the row is absent SILENTLY.
 *
 * Not a refusal, and not a row saying the reading is unavailable: a fact that is
 * none of somebody's work is not a fact being withheld from them, and a permission
 * boundary drawn at the foot of every screen they open would be. The request is
 * never made either — the grant is read from the session the shell already holds.
 *
 * This frame is also the one that shows what is LEFT when the row goes, which is
 * the rule the foot's own chrome is drawn by rather than a state to admire.
 */
export const RailEntitlementWithoutTheGrant: Story = {
  name: "the foot — no license:read",
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <SeedInstallation>
          <SidebarExample initiallyCollapsed={false} />
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

/**
 * The sidebar's SECOND level.
 *
 * The section below is a fixture, and deliberately so: at runtime the settings
 * screen publishes this shape from live grants (`useSettingsSection`), which
 * would make these stories a picture of a permission matrix rather than of the
 * level. The point here is what the SHELL does with a section — one level at a
 * time, the reduced head with the way back under the mark, the two groups under
 * the section's own name — so the data is held still and the rendering is what
 * varies.
 *
 * `privacy` carries children no settings entry really has, which is the one part
 * of the fixture that is not a copy of production: it is how the third level
 * (and the back control that names the level it leads to) can be seen at all.
 */
const SETTINGS_SECTION: NavSection = {
  screen: "settings",
  titleKey: "nav.settings",
  activeId: "privacy",
  groups: [
    {
      headingKey: "settings.group.you",
      items: [
        { id: "account", labelKey: "settings.tab.account", icon: UserRound },
        { id: "voice", labelKey: "settings.tab.voice", icon: Mic },
        { id: "agents", labelKey: "settings.tab.agents", icon: KeyRound },
      ],
    },
    {
      headingKey: "settings.group.admin",
      items: [
        { id: "general", labelKey: "settings.tab.general", icon: Building2 },
        { id: "people", labelKey: "settings.tab.people", icon: UsersRound },
        { id: "connections", labelKey: "settings.tab.connections", icon: Plug },
        { id: "capture", labelKey: "settings.tab.capture", icon: Mail },
        {
          id: "data-model",
          labelKey: "settings.tab.data-model",
          icon: Database,
        },
        { id: "ai", labelKey: "settings.tab.ai", icon: Sparkles },
        {
          id: "privacy",
          labelKey: "settings.tab.privacy",
          icon: ShieldCheck,
          children: [
            { id: "people", labelKey: "settings.tab.people", icon: UsersRound },
            {
              id: "data-model",
              labelKey: "settings.tab.data-model",
              icon: Database,
            },
          ],
        },
        {
          id: "maintenance",
          labelKey: "settings.tab.maintenance",
          icon: Wrench,
        },
      ],
    },
  ],
};
// The level shown is derived from the ADDRESS, and the way back up navigates —
// so in a story holding a route still it moves the URL and leaves the panel
// where the story put it. Each depth below is therefore a story of its own,
// which is also how a reviewer sees them side by side.
//
// A level takes the brand's words for its rows and nothing else. The search and
// the collapse control belong to the top bar above, so the story renders that bar
// too — a panel shown without it would be a picture of half the chrome.
function LevelExample({
  initiallyCollapsed,
  tab = "general",
  sub,
}: Readonly<{ initiallyCollapsed: boolean; tab?: string; sub?: string }>) {
  const [collapsed, setCollapsed] = useState(initiallyCollapsed);
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <WorkspaceRail
        route={{ screen: "settings", id: tab, id2: sub }}
        section={{ ...SETTINGS_SECTION, activeId: tab }}
        counts={COUNTS}
        collapsed={collapsed}
      />
      <main className="main">
        <TopBar
          route={{ screen: "settings", id: tab, id2: sub }}
          section={{ ...SETTINGS_SECTION, activeId: tab }}
          counts={COUNTS}
          collapsed={collapsed}
          onToggle={() => setCollapsed((current) => !current)}
          onOpenSearch={openSearch}
        />
        <div className="scroll" />
      </main>
      {palette}
    </div>
  );
}

function LevelStory({ children }: Readonly<{ children: ReactNode }>) {
  stubSession();
  return (
    <StoryProviders>
      <SeedInstallation>{children}</SeedInstallation>
    </StoryProviders>
  );
}

// The labeled level: the logomark, the way back up, the section's name, then its
// two groups. The ten destinations are GONE rather than pushed below a second
// list — 252px carrying both levels reads as a list of twenty places to go —
// while the head keeps the mark and gives up only the brand's words. The search
// is not in this panel at any level: it belongs to the strip above, and the way
// back up is the only control the head adds.
export const SectionLevel: Story = {
  name: "second level — expanded",
  render: () => (
    <LevelStory>
      <LevelExample initiallyCollapsed={false} />
    </LevelStory>
  ),
};

// The same level at 64px: icons, the collapsed rail's tooltip on hover or
// keyboard focus, group headings reduced to hairlines, and the section's own
// name clipped for the eye while a screen reader still reads it.
export const SectionLevelCollapsed: Story = {
  name: "second level — collapsed",
  render: () => (
    <LevelStory>
      <LevelExample initiallyCollapsed />
    </LevelStory>
  ),
};

// The third level, reached by standing on an entry that has children: the level
// is named by the entry the reader drilled through, and the back control names
// the list it leads back to.
export const ThirdLevel: Story = {
  name: "third level — expanded",
  render: () => (
    <LevelStory>
      {/* The child segment too, or the third level renders with no row current
          — a picture of a level nobody is standing in. */}
      <LevelExample initiallyCollapsed={false} tab="privacy" sub="data-model" />
    </LevelStory>
  ),
};

/**
 * A section at phone width, where the sidebar does NOT hand its bar over.
 *
 * The bar keeps the four destinations plus More on a section route — handing them
 * to a section's entries lost the whole product's navigation and left two
 * controls floating in a card — so the section lives in the PAGE HEAD here: the
 * heading names it, and the switcher under the heading names the entry the reader
 * is on and opens the rest as a sheet.
 *
 * Rendered from the parts rather than through `Shell`, because the real settings
 * section is published from live grants: the rail and the head are both handed the
 * fixture, which is what keeps this a picture of the CHROME.
 *
 * The bar itself carries five glyphs and no captions — four destinations and
 * More. Every row keeps its name in `aria-label`, so nothing is lost to a screen
 * reader or a voice-control user; the words are simply not drawn at a size where
 * they would be legible, and the sheet behind More is where they come back.
 *
 * One thing about the viewport tool is worth knowing before trusting a picture
 * of this: it is applied by the MANAGER, which resizes the preview iframe, and
 * these are viewport media queries — so a story opened as a bare `iframe.html`,
 * which is how the fe-uat capture gate renders, would get the harness's own width
 * and draw the SIDEBAR. `uat-phone` is what stops that: the gate reads the tag and
 * drives the browser itself to 390px. Without it every story named for a phone was
 * captured at 1024px, which is how three of them once shipped showing a sidebar.
 */
function PhoneSectionExample() {
  const route: Route = { screen: "settings", id: "privacy" };
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className="app railexpanded">
      <WorkspaceRail route={route} section={SETTINGS_SECTION} counts={COUNTS} />
      <main className="main">
        <TopBar
          route={route}
          section={SETTINGS_SECTION}
          counts={COUNTS}
          collapsed={false}
          onOpenSearch={openSearch}
        />
        <div className="scroll">
          <PageTitle route={route} section={SETTINGS_SECTION} />
          <div className="wrap">
            <Card as="div">Content</Card>
          </div>
        </div>
      </main>
      {palette}
    </div>
  );
}

export const SectionPhone: Story = {
  name: "a section — phone bar and the head's switcher",
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => (
    <LevelStory>
      <PhoneSectionExample />
    </LevelStory>
  ),
};

/**
 * The More sheet, open — the phone's whole sidebar.
 *
 * The bar it grew out of is five glyphs with no captions, which is all a 390px
 * row can hold; the sheet is where the ten destinations become a LIST again, and
 * everything the bar had to give up comes back with them:
 *
 * - a head, with the installation's mark and name — the bar has no room for one,
 *   so this is the only place at this width that says which workspace this is;
 * - the labels themselves, at body size, with each row's glyph on the left and
 *   the whole width as its target;
 * - the group headings, spelled out rather than standing in as the collapsed
 *   rail's hairline, because the sheet is 600px wide whatever the desktop
 *   preference it inherited was left at;
 * - the badge as a TRAILING figure in its row rather than a chip pinned to a
 *   tab — a list row has somewhere to put a number;
 * - the entitlement row in the foot, because what the installation is entitled
 *   to is as true on a phone as anywhere.
 *
 * That last one is below the fold in the captured frame, honestly: the sheet is
 * `max-height: 80dvh` over 821px of content, so it is a scroller, and the foot
 * sits about 70px past its bottom edge. It is there and it is reachable — scroll
 * the sheet — but a screenshot of the sheet's head is not a screenshot of its
 * foot, and saying so is cheaper than a second frame scrolled to the end.
 *
 * The close control is the same More button in its other state, renamed, and it
 * is PINNED to the head's corner rather than left at the end of a scrolling list
 * — a reader who opened the sheet by mistake should not have to walk ten rows to
 * leave it. There is deliberately no grab handle: a handle is a control on every
 * platform that documents one, and this sheet has one height and no drag, so
 * drawing it would promise a gesture that does nothing.
 *
 * The route is Reports, which the bar does not carry. That is the case the sheet
 * exists to answer: the current page's own row is `display: none` in the bar, so
 * the closed bar reports it through More instead, and opening the sheet is what
 * makes it visible. It is also why the scrim and the `inert` content column are
 * part of the frame rather than decoration — the page under a sheet this size has
 * to stop being reachable by Tab, not merely be dimmed.
 *
 * `uat-phone` is what makes the capture gate drive the browser to 390px, and the
 * `play` below opens the sheet the way a reader does; there is no prop that forces
 * it open, and adding one would put a control in the product that exists only for
 * the catalog. The focus ring on the first row in the captured frame is that
 * opening, not a stray state: a sheet that covers the page has to take focus with
 * it, or a keyboard reader is left tabbing through a page they can no longer see.
 */
function PhoneSheetExample() {
  const route: Route = { screen: "reports" };
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className="app railexpanded">
      <WorkspaceRail route={route} counts={COUNTS} />
      <main className="main">
        <TopBar
          route={route}
          counts={COUNTS}
          collapsed={false}
          onOpenSearch={openSearch}
        />
        <div className="scroll">
          <PageTitle route={route} />
          <div className="wrap">
            <Card as="div">The page the sheet covers.</Card>
          </div>
        </div>
      </main>
      {palette}
    </div>
  );
}

export const PhoneMoreSheet: Story = {
  name: "phone — the More sheet as a list",
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "More" }));
  },
  render: () => {
    stubEntitlement(WITHIN_CAP);
    return (
      <StoryProviders>
        <SeedInstallation>
          <PhoneSheetExample />
        </SeedInstallation>
      </StoryProviders>
    );
  },
};
