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
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import type { NavSection } from "./nav";
import { CommandPalette, useBuiltinCommands } from "./palette";
import { PageHead, Shell, WorkspaceRail } from "./shell";

// fullscreen: the shell sizes itself to the viewport, so Storybook's default
// canvas padding would clip the sidebar foot and misrepresent the layout — and
// the foot is where the account block now lives.
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
      headingKey: "settings.group.org",
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
// A level takes the brand's words for its rows and nothing else: the search row
// is still there, so the palette is wired here exactly as it is above — a row
// that opened nothing would misrepresent the one visible way to search.
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

// The labeled level: the logomark, the search row, the way back up, the section's
// name, then its two groups. The ten destinations are GONE rather than pushed
// below a second list — 252px carrying both levels reads as a list of twenty
// places to go — while the head keeps the mark and the search row, and gives up
// only the brand's words.
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
 * One thing about the viewport tool is worth knowing before trusting a picture
 * of this: it is applied by the MANAGER, which resizes the preview iframe. These
 * are viewport media queries, so nothing inside the preview can stand in for
 * that — a story opened as a bare `iframe.html`, which is how the fe-uat capture
 * gate renders, gets the harness's own width and draws the SIDEBAR. Review this
 * one in Storybook itself, or by narrowing the browser.
 */
function PhoneSectionExample() {
  const route = { screen: "settings", id: "privacy" };
  const { openSearch, palette } = usePaletteSeam();
  return (
    <div className="app railexpanded">
      <WorkspaceRail
        route={route}
        section={SETTINGS_SECTION}
        counts={{ inbox: 12, tasks: 4 }}
        onOpenSearch={openSearch}
      />
      <main className="main">
        <PageHead route={route} section={SETTINGS_SECTION} />
        <div className="scroll">
          <div className="wrap">
            <div className="card">Content</div>
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
  render: () => (
    <LevelStory>
      <PhoneSectionExample />
    </LevelStory>
  ),
};
