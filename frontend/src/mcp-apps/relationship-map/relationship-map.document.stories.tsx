import type { Meta, StoryObj } from "@storybook/react-vite";
import { builtDocument, DocumentHost } from "../document-host";
import { relationshipMapFixture } from "./fixture";

// LAZY, via import.meta.glob — see document-host.tsx.
const built = import.meta.glob("/dist/mcp-apps/*.html", {
  query: "?raw",
  import: "default",
  eager: true,
});

const meta: Meta<typeof DocumentHost> = {
  title: "MCP Apps/Relationship map (document)",
  component: DocumentHost,
  args: {
    html: builtDocument(built, "relationship-map"),
    answer: relationshipMapFixture,
    title: "Who knows this contact",
  },
};
export default meta;

type Story = StoryObj<typeof DocumentHost>;

export const InALightHost: Story = { args: { theme: "light" } };

export const InADarkHost: Story = { args: { theme: "dark" } };
