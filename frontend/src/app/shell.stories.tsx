import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { CommandPalette, useBuiltinCommands } from "./palette";
import { Shell, WorkspaceRail } from "./shell";

// fullscreen: the shell sizes itself to the viewport, so Storybook's default
// canvas padding would clip the sidebar foot and misrepresent the layout — and
// the foot is where the account block now lives.
const meta: Meta<typeof Shell> = {
  title: "App/Shell",
  component: Shell,
  parameters: { layout: "fullscreen" },
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
