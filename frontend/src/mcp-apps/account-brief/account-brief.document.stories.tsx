import type { Meta, StoryObj } from "@storybook/react-vite";
import { builtDocument, DocumentHost } from "../document-host";
import { accountBriefFixture } from "./fixture";

// LAZY, via import.meta.glob — see document-host.tsx for why a static `?raw`
// import of an absent file would fail the whole Storybook build.
const built = import.meta.glob("/dist/mcp-apps/*.html", {
  query: "?raw",
  import: "default",
  eager: true,
});

const meta: Meta<typeof DocumentHost> = {
  title: "MCP Apps/Account brief (document)",
  component: DocumentHost,
  args: {
    html: builtDocument(built, "account-brief"),
    answer: accountBriefFixture,
    title: "Morning brief",
  },
};
export default meta;

type Story = StoryObj<typeof DocumentHost>;

/** The real built bytes, sandboxed the way a host sandboxes them, driven through
 *  the real handshake. This is the fidelity check the renderer stories cannot be:
 *  they render against Storybook's ambient app.css, and this does not. */
export const InALightHost: Story = { args: { theme: "light" } };

export const InADarkHost: Story = { args: { theme: "dark" } };
