import type { Meta, StoryObj } from "@storybook/react-vite";
import { ViewHost } from "../story-hosts";
import { pipelineReviewFixture } from "./fixture";
import { render } from "./main";

const meta: Meta<typeof ViewHost> = {
  title: "MCP Apps/Pipeline review",
  component: ViewHost,
  args: { render },
};
export default meta;

type Story = StoryObj<typeof ViewHost>;

export const Populated: Story = { args: { data: pipelineReviewFixture.data } };

/** No deal whose risk can be evidenced from its own fields is the answer, not
 *  a gap. */
export const Empty: Story = { args: { data: { deals: [] } } };

/** The tool never answers an unevidenced deal, so a row with no evidence says
 *  so rather than showing a rank with no reason behind it. */
export const NoEvidence: Story = {
  args: {
    data: {
      deals: [
        {
          deal_id: "9b2ffd94-0a1c-4b73-8e5d-6f7a8b9c0d1e",
          name: "Unexplained deal",
          evidence: [],
        },
      ],
    },
  },
};

export const WrongShape: Story = { args: { data: 42 } };
