import type { Meta, StoryObj } from "@storybook/react-vite";
import { ViewHost } from "../story-hosts";
import { accountBriefFixture } from "./fixture";
import { render } from "./main";

const meta: Meta<typeof ViewHost> = {
  title: "MCP Apps/Account brief",
  component: ViewHost,
  args: { render },
};
export default meta;

type Story = StoryObj<typeof ViewHost>;

export const Populated: Story = { args: { data: accountBriefFixture.data } };

/** An empty queue is an ANSWER, not a failure, and the view has to say so. */
export const Empty: Story = {
  args: { data: { items: [], candidate_count: 0 } },
};

/** candidate_count above the queue length is what the ranking left out. */
export const MoreCandidatesThanQueued: Story = {
  args: {
    data: { ...(accountBriefFixture.data as object), candidate_count: 40 },
  },
};

/** A factor the seam did not send renders as an em dash — never as NaN, and
 *  never as a zero the reader would take for a measurement. */
export const MissingFactor: Story = {
  args: {
    data: {
      candidate_count: 1,
      items: [
        {
          deal_id: "8f14e45f-ceea-467a-9a1a-2e9b0e4c3d21",
          rank: 1,
          composite: 0.5,
          factors: { winnability: 0.9 },
          state: "new",
        },
      ],
    },
  },
};

/** A payload of the wrong shape falls to the empty state rather than throwing a
 *  blank panel at the reader. */
export const WrongShape: Story = { args: { data: "not an object" } };
