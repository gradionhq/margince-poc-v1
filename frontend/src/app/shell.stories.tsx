import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import {
  Building2,
  Database,
  Layers,
  Mic,
  ScrollText,
  ShieldCheck,
  Sparkles,
  UsersRound,
  Webhook,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import type { NavSection } from "./nav";
import { CommandPalette, useBuiltinCommands } from "./palette";
import { Shell, WorkspaceRail } from "./shell";

// fullscreen: the shell sizes itself to the viewport, so Storybook's default
// canvas padding would clip the sidebar foot and misrepresent the layout — and
// the foot is where the account block now lives.
const meta: Meta<typeof Shell> = {
  title: "App/Shell",
  component: Shell,
  parameters: {
    layout: "fullscreen",
    // The phone story at the foot of this file needs a WIDTH: the bottom-bar
    // rules are viewport media queries. Named after the RULE rather than after a
    // device, and the viewport tool ships inside Storybook 9 itself, so this
    // adds no addon to `.storybook/main.ts`.
    viewport: {
      options: {
        phone: {
          name: "Phone (max 700px)",
          styles: { width: "390px", height: "844px" },
        },
      },
    },
  },
};
export default meta;
type Story = StoryObj<typeof Shell>;

function stubSession() {
  installFetchStub({
    "GET /me": () =>
      jsonResponse({
        user: { id: "u1", email: "admin@example.test", display_name: "Admin" },
        roles: ["admin"],
      }),
    "GET /ai/usage": () =>
      jsonResponse({
        days: [],
        budget: { monthly_tokens: 100, spent_tokens: 20, band: "normal" },
      }),
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
 * `onOpenSearch` is not a decorative prop: the sidebar's first row is the only
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
      <Shell onOpenSearch={openSearch} counts={{ inbox: 12, tasks: 4 }}>
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
              <div className="card">Content</div>
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
        counts={{ inbox: 12, tasks: 4 }}
        collapsed={collapsed}
        onToggle={() => setCollapsed((current) => !current)}
        onOpenSearch={openSearch}
      />
      <main className="main">
        <div className="scroll" />
      </main>
      {palette}
    </div>
  );
}

// Both sidebar states, side by side. Collapsed is the canonical 64px geometry:
// 44x44 targets, the logomark chip, the active indicator, group headings
// reduced to hairline rules — and search and the account block reduced to the
// glyph and the avatar, which is what has to stay legible at that width.
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
 * The sidebar's SECOND level.
 *
 * The section below is a fixture, and deliberately so: at runtime the settings
 * screen publishes this shape from live grants (`useSettingsSection`), which
 * would make these stories a picture of a permission matrix rather than of the
 * level. The point here is what the SHELL does with a section — one level at a
 * time, the way back up above the entries, the two groups under the section's
 * own name — so the data is held still and the rendering is what varies.
 *
 * `overlay` carries children no settings tab really has, which is the one part
 * of the fixture that is not a copy of production: it is how the third level
 * (and the back control that names the level it leads to) can be seen at all.
 */
const SETTINGS_SECTION: NavSection = {
  screen: "settings",
  titleKey: "nav.settings",
  activeId: "audit",
  groups: [
    {
      headingKey: "settings.group.you",
      items: [
        { id: "account", labelKey: "settings.tab.account", icon: Building2 },
        { id: "voice", labelKey: "settings.tab.voice", icon: Mic },
        { id: "ai", labelKey: "settings.tab.ai", icon: Sparkles },
        {
          id: "integrations",
          labelKey: "settings.tab.integrations",
          icon: Webhook,
        },
      ],
    },
    {
      headingKey: "settings.group.org",
      items: [
        { id: "users", labelKey: "settings.tab.users", icon: UsersRound },
        { id: "data", labelKey: "settings.tab.data", icon: Database },
        { id: "privacy", labelKey: "settings.tab.privacy", icon: ShieldCheck },
        { id: "audit", labelKey: "settings.tab.audit", icon: ScrollText },
        {
          id: "overlay",
          labelKey: "settings.tab.overlay",
          icon: Layers,
          children: [
            { id: "users", labelKey: "settings.tab.users", icon: UsersRound },
            { id: "data", labelKey: "settings.tab.data", icon: Database },
          ],
        },
      ],
    },
  ],
};

// The back control is LIVE in every story below: walking up to the destinations
// and back is the interaction the level exists for, and a story where it did
// nothing would hide it.
function LevelExample({
  initiallyCollapsed,
  tab = "audit",
}: Readonly<{ initiallyCollapsed: boolean; tab?: string }>) {
  const [collapsed, setCollapsed] = useState(initiallyCollapsed);
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <WorkspaceRail
        route={{ screen: "settings", id: tab }}
        section={{ ...SETTINGS_SECTION, activeId: tab }}
        counts={{ inbox: 12, tasks: 4 }}
        collapsed={collapsed}
        onToggle={() => setCollapsed((current) => !current)}
        onOpenSearch={openSearch}
      />
      <main className="main">
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

// The labeled level: the way back up, the section's name, then its two groups.
// The ten destinations are GONE rather than pushed below a second list — 252px
// carrying both levels reads as a list of twenty places to go.
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
      <LevelExample initiallyCollapsed={false} tab="overlay" />
    </LevelStory>
  ),
};

/**
 * The level at phone width: a bar of two controls — the way back up, and More
 * opening the sheet at the level the panel is showing, so the section's entries
 * stay one tap away and the destinations one tap behind the back row.
 *
 * One thing about the viewport tool is worth knowing before trusting a picture
 * of this: it is applied by the MANAGER, which resizes the preview iframe. These
 * are viewport media queries, so nothing inside the preview can stand in for
 * that — a story opened as a bare `iframe.html`, which is how the fe-uat capture
 * gate renders, gets the harness's own width and draws the SIDEBAR. Review this
 * one in Storybook itself, or by narrowing the browser.
 */
export const SectionLevelPhone: Story = {
  name: "second level — phone bar and sheet",
  globals: { viewport: { value: "phone" } },
  render: () => (
    <LevelStory>
      <LevelExample initiallyCollapsed={false} />
    </LevelStory>
  ),
};
