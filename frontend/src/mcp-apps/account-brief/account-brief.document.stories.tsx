import type { Meta, StoryObj } from "@storybook/react-vite";
import { builtDocument, DocumentHost } from "../story-hosts";
import { accountBriefFixture } from "./fixture";

// import.meta.glob, NOT a static `?raw` import. A static import of an absent
// file is a module-resolution error: Storybook would fail to BUILD when the
// documents have not been produced, taking the CI frontend job down for
// unrelated changes — and it could never render the "run pnpm build" panel this
// story promises. `eager` is fine and is not the point: a glob that matches
// nothing is an empty object, which is exactly the case being handled.
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
