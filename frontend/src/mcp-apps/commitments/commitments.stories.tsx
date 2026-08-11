import type { Meta, StoryObj } from "@storybook/react-vite";
import { ViewHost } from "../story-hosts";
import { commitmentsFixture } from "./fixture";
import { render } from "./main";

const meta: Meta<typeof ViewHost> = {
  title: "MCP Apps/Open commitments",
  component: ViewHost,
  args: { render },
};
export default meta;

type Story = StoryObj<typeof ViewHost>;

export const Populated: Story = { args: { data: commitmentsFixture.data } };

/** Nothing outstanding is the answer, not a gap — and the panel still says
 *  that only promises written down as tasks were counted. */
export const Empty: Story = {
  args: { data: { as_of: "2026-06-10T12:00:00Z", commitments: [] } },
};

/** A capped sweep stops claiming to be everything outstanding. */
export const Truncated: Story = {
  args: {
    data: commitmentsFixture.data,
    warnings: [{ code: "sweep_truncated" }],
  },
};

/** A state the seam has not published yet renders with no colour rather than
 *  the wrong one. */
export const UnknownState: Story = {
  args: {
    data: {
      as_of: "2026-06-10T12:00:00Z",
      commitments: [
        { subject: "Escalate the renewal", state: "escalated", about: [] },
      ],
    },
  },
};

export const WrongShape: Story = { args: { data: 42 } };
