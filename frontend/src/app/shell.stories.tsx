import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { Shell, WorkspaceRail } from "./shell";

// fullscreen: the shell sizes itself to the viewport, so Storybook's default
// canvas padding would clip the sidebar foot and misrepresent the layout.
const meta: Meta<typeof Shell> = {
  title: "app/shell",
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

export const Default: Story = {
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <SeedInstallation>
          <Shell onOpenSearch={() => {}} counts={{ inbox: 12, tasks: 4 }}>
            <div className="wrap">
              <div className="card">Content</div>
            </div>
          </Shell>
        </SeedInstallation>
      </StoryProviders>
    );
  },
};

// Both sidebar states, side by side. Collapsed is the canonical 64px geometry:
// 44x44 targets, the logomark chip, the active indicator, group headings
// reduced to hairline rules.
export const SidebarStates: Story = {
  name: "sidebar — expanded and collapsed",
  render: () => {
    stubSession();
    return (
      <StoryProviders>
        <SeedInstallation>
          <div style={{ display: "flex", height: "100vh" }}>
            <div style={{ width: 296, display: "flex" }}>
              <WorkspaceRail
                route={{ screen: "deals" }}
                counts={{ inbox: 12, tasks: 4 }}
                onToggle={() => {}}
              />
            </div>
            <div style={{ width: 64, display: "flex" }}>
              <WorkspaceRail
                route={{ screen: "deals" }}
                counts={{ inbox: 12, tasks: 4 }}
                collapsed
                onToggle={() => {}}
              />
            </div>
          </div>
        </SeedInstallation>
      </StoryProviders>
    );
  },
};
