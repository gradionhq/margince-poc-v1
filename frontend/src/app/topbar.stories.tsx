// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import { Database, ShieldCheck, UserRound } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Card } from "../design-system/atoms";
import {
  installFetchStub,
  meRoute,
  StoryProviders,
} from "../screens/story-utils";
import type { NavSection } from "./nav";
import { CommandPalette, useBuiltinCommands } from "./palette";
import type { Route } from "./router";
import { PageTitle, WorkspaceRail } from "./shell";
import { TopBar } from "./topbar";

/**
 * The one strip that is true of the whole session rather than of the page under
 * it: where you are, how you reach anything, which system of record is
 * answering, and who is signed in.
 *
 * Every frame renders the REAL frame — the `.app` grid, the sidebar beside it,
 * the bar as the first row of `<main>`. Nothing here is decoration: the trail
 * measures itself against whatever the lead column is left with, and the search
 * is absolutely positioned at `calc(50vw - var(--railW))` inside a column that
 * starts at `--railW`, which is how it lands on the centre of the WINDOW and
 * stays there when the sidebar moves. A bar rendered at a hand-set width is a
 * picture of geometry the product does not use, and the one thing it would get
 * wrong is the thing worth looking at.
 *
 * The sidebar control is live in every frame, so either state can be moved to
 * the other and the search can be watched staying put while the column's edge
 * travels.
 *
 * ONE thing these frames cannot promise on their own. The centred field is a
 * `min-width: 1101px` arrangement, and `fe-uat` captures every non-phone story
 * at 1024px — already inside the narrow rule. So the screenshots the render
 * gate takes of the wide frames below show the collapsed glyph, and the centred
 * field is real only in Storybook itself, in a window wider than 1100px. The
 * `Narrow` story is the one that pins the small arrangement deterministically.
 *
 * fullscreen: the bar measures itself against the viewport at both ends — the
 * search against `50vw`, the trail flush to the right edge — so Storybook's
 * default canvas padding would frame a geometry the product does not use.
 */
const meta: Meta<typeof TopBar> = {
  title: "Shell/Top bar",
  component: TopBar,
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj<typeof TopBar>;

// The badges the rail beside the bar carries. They belong to the sidebar rather
// than to this strip, and they are here so the frame is the real one.
const COUNTS = { inbox: 12, tasks: 4 };

/**
 * The width the bar's own media query turns on at, for the two frames that are
 * only true below it.
 *
 * Declared per story rather than in the shared preview config, and named after
 * the RULE rather than a device: 960px is a width inside `max-width: 1100px`,
 * which is where the centred field gives up its slot. Storybook's viewport tool
 * is applied by the MANAGER, which resizes the preview iframe from outside it,
 * so these frames are honest in Storybook. They are honest under the render gate
 * too, for an unrelated reason: `fe-uat` drives every non-phone story at 1024px,
 * which is already inside this same rule.
 */
const NARROW_VIEWPORT = {
  viewport: {
    options: {
      narrow: {
        name: "Narrow (max 1100px)",
        styles: { width: "960px", height: "720px" },
      },
    },
  },
};

/**
 * The session the bar's own trail-end needs: the account block reads `GET /me`,
 * and so does the system-of-record chip beside it.
 *
 * `meRoute({})` is an admin holding no object grants — a real principal, and the
 * right one here, because nothing in this strip is grant-dependent.
 */
function stubSession() {
  installFetchStub({ "GET /me": meRoute({}) });
}

/**
 * The two cache entries the chrome around the bar reads, seeded rather than
 * fetched.
 *
 * `["company"]` is the installation profile the onboarding gate fills in the
 * real app; without it the sidebar's brand block honestly shows the product name
 * alone. `[kind, "ref", id]` is `useEntityName`'s entry, which is where the last
 * segment of a record's trail comes from — the trail is a READ, and a story that
 * left it unresolved would be showing a uuid where the product shows a name.
 */
function SeedCache({
  record,
  children,
}: Readonly<{
  record?: { id: string; name: string };
  children: ReactNode;
}>) {
  const client = useQueryClient();
  if (client.getQueryData(["company"]) === undefined) {
    client.setQueryData(["company"], {
      organization_id: "org-1",
      display_name: "Gradion GmbH",
    });
  }
  if (
    record &&
    client.getQueryData(["person", "ref", record.id]) === undefined
  ) {
    client.setQueryData(["person", "ref", record.id], record.name);
  }
  return <>{children}</>;
}

/**
 * The search row wired to the thing it actually opens.
 *
 * `onOpenSearch` is not a decorative prop: this field is the only pointer route
 * into the command palette, so a story that stubbed it would show a control that
 * does nothing and prove nothing. The app mounts the palette beside the shell
 * (App.tsx) — so does this, off the same builtin command list.
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

/**
 * The bar in the chrome it is the first row of.
 *
 * The page's own heading is rendered under it on purpose: the trail and the h1
 * answer the same question from one place (`app/pagemeta.ts`), and the frame is
 * where a reviewer can see they never name the page two different things. On a
 * record route the heading is absent by design — the record page names itself —
 * which is why the content card stands in below.
 */
function BarFrame({
  route,
  section,
  startCollapsed = false,
}: Readonly<{
  route: Route;
  section?: NavSection;
  startCollapsed?: boolean;
}>) {
  const [collapsed, setCollapsed] = useState(startCollapsed);
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <WorkspaceRail
        route={route}
        section={section}
        counts={COUNTS}
        collapsed={collapsed}
      />
      <main className="main">
        <TopBar
          route={route}
          section={section}
          collapsed={collapsed}
          onToggle={() => setCollapsed((current) => !current)}
          onOpenSearch={openSearch}
        />
        <div className="scroll">
          <PageTitle route={route} section={section} />
          <div className="wrap">
            <Card as="div">The page, under the strip that names it.</Card>
          </div>
        </div>
      </main>
      {palette}
    </div>
  );
}

function BarStory({
  route,
  section,
  startCollapsed,
  record,
}: Readonly<{
  route: Route;
  section?: NavSection;
  startCollapsed?: boolean;
  record?: { id: string; name: string };
}>) {
  stubSession();
  return (
    <StoryProviders>
      <SeedCache record={record}>
        <BarFrame
          route={route}
          section={section}
          startCollapsed={startCollapsed}
        />
      </SeedCache>
    </StoryProviders>
  );
}

/**
 * A list route: one crumb, naming the page and linking nowhere.
 *
 * The last stop is never a link even when it carries an href — a link to the
 * page you are already on is a control that does nothing — so a one-stop trail
 * is a plain, current label. There is no slash, because a separator earns its
 * place only by having something to separate.
 */
export const ListRoute: Story = {
  name: "a list route — one crumb",
  render: () => <BarStory route={{ screen: "deals" }} />,
};

/**
 * The same route with the sidebar at 64px.
 *
 * This is the frame the centring claim is made against: the content column's
 * left edge has moved 188px and the search has moved with it, staying at the
 * middle of the column it belongs to. `--railW` is a registered length, so the
 * bar's grid glides rather than snapping to the new width.
 */
export const ListRouteCollapsed: Story = {
  name: "a list route — sidebar collapsed",
  render: () => <BarStory route={{ screen: "deals" }} startCollapsed />,
};

/**
 * A record: two segments, the last one the record's NAME.
 *
 * The name is a read, which is the whole reason the trail is a hook — a crumb
 * that printed the uuid until the read landed would be a different sentence on
 * every page open. The first stop leads back to the list the record was opened
 * from, which is the one place a reader can go from here that is not somewhere
 * else entirely.
 */
export const RecordRoute: Story = {
  name: "a record — the trail ends in its name",
  render: () => (
    <BarStory
      route={{ screen: "contacts", id: "p-anna" }}
      record={{ id: "p-anna", name: "Anna Weber" }}
    />
  ),
};

/**
 * A record whose name will not fit, which is the case the trail's shrink rules
 * exist for.
 *
 * Two things are visible at once. The ancestor keeps its natural width — it is
 * the product's own nav label, short and already chosen, and shortening it would
 * hide the way back rather than the overflow — while the last stop, the only
 * segment of unbounded length, is the one that gives way and ellipses. And the
 * row does not grow: the strip is a fixed-height chrome band, so the name is cut
 * rather than allowed to push the search or the account block off the end. The
 * rest of the name is on the tooltip the clipped label carries, on hover and on
 * keyboard focus alike.
 *
 * True at every width, and it was not always: the search was `position: absolute`
 * once, which left nothing bounding `.topbar-lead` but the row itself — the last
 * crumb grew to the full width of the bar, ran UNDER the centred field and came
 * out the other side, unclipped and with no tooltip. The bar is a three-track
 * grid now (`minmax(0, 1fr) auto minmax(0, 1fr)`), so the trail has a bound to
 * shrink against and this frame shows the rule rather than the bug.
 */
export const RecordLongName: Story = {
  name: "a record whose name outruns the row",
  render: () => (
    <BarStory
      route={{ screen: "contacts", id: "p-long" }}
      record={{
        id: "p-long",
        name:
          "Maria Aleksandra Wittenberg-Sørensen de la Cruz Habermann-Vogelsang " +
          "(Beteiligungsgesellschaft für erneuerbare Energien)",
      }}
    />
  ),
};

/**
 * The settings section the sidebar publishes, held still.
 *
 * At runtime the settings screen builds this from live grants, which would make
 * the story a picture of a permission matrix rather than of the trail. Held
 * still, what varies is the rendering: the section's own name leads, the entry
 * the reader opened closes the trail, and the heading below names that entry
 * ONCE — the section is already named by the level in the sidebar and by the
 * crumb above, and printing it a third time is how a settings page came to read
 * "Settings" over a heading reading "Settings".
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
      ],
    },
    {
      headingKey: "settings.group.org",
      items: [
        { id: "privacy", labelKey: "settings.tab.privacy", icon: ShieldCheck },
        {
          id: "data-model",
          labelKey: "settings.tab.data-model",
          icon: Database,
        },
      ],
    },
  ],
};

export const SettingsEntry: Story = {
  name: "a settings entry — Settings / the entry",
  render: () => (
    <BarStory
      route={{ screen: "settings", id: "privacy" }}
      section={SETTINGS_SECTION}
    />
  ),
};

/**
 * Under 1100px, where a centred field and the trail beside it are competing for
 * the same pixels.
 *
 * The search keeps its place in the row and drops to its glyph, and the trail is
 * what the width is spent on — it is the half a reader cannot recover from a
 * keyboard shortcut. The affordance itself never leaves the bar: on touch there
 * is no ⌘K to fall back on, so a search that vanished at narrow widths would be
 * a search with no way in at all.
 *
 * It is also the arrangement in which the trail is bounded at all: with the
 * search back in the flow, the lead column ends where the glyph begins, and the
 * crumb clips against that edge instead of running past it.
 */
export const Narrow: Story = {
  name: "under 1100px — search drops to its glyph",
  parameters: NARROW_VIEWPORT,
  globals: { viewport: { value: "narrow" } },
  render: () => (
    <BarStory
      route={{ screen: "contacts", id: "p-anna" }}
      record={{ id: "p-anna", name: "Anna Weber" }}
    />
  ),
};

/**
 * Phone width, where the sidebar is the bottom bar.
 *
 * There is no panel left to collapse, so the control for it is gone rather than
 * disabled — and what stays is exactly what a phone reader cannot get any other
 * way: where they are, how to search, and who they are. The bar is shorter and
 * its gutters narrower, which is the whole of what it gives up.
 *
 * `uat-phone` is what makes the capture gate drive the browser to 390px. Without
 * it the frame would be captured at the harness's own width and would draw the
 * SIDEBAR — every story named for a phone was captured that way once. A reviewer
 * reads this one in Storybook with the viewport tool, or by narrowing the window.
 */
export const Phone: Story = {
  name: "phone — no collapse control",
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => <BarStory route={{ screen: "deals" }} />,
};
