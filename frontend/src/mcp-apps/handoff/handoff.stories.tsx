import type { Meta, StoryObj } from "@storybook/react-vite";
import { ViewHost } from "../story-hosts";
import { handoffFixture } from "./fixture";
import { render } from "./main";

const meta: Meta<typeof ViewHost> = {
  title: "MCP Apps/Delivery handoff",
  component: ViewHost,
  args: { render },
};
export default meta;

type Story = StoryObj<typeof ViewHost>;

/** A handover that is NOT ready: five gaps, each naming the field it was read
 *  off. This is the case the view exists for. */
export const NotReady: Story = { args: { data: handoffFixture.data } };

/** Nothing the records were checked for is missing. */
export const Ready: Story = {
  args: {
    data: {
      ...(handoffFixture.data as Record<string, unknown>),
      owner_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
      target_end_date: "2026-09-30T00:00:00Z",
      gaps: [],
    },
  },
};

/** A project with nothing rolled up to it yet: the gaps say so, and no section
 *  heads a void. */
export const Bare: Story = {
  args: {
    data: {
      project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
      name: "Initech discovery",
      phase: "pursuing",
      gaps: [
        {
          code: "no_won_deal",
          source: "deal.status",
          message:
            "No won deal is rolled up to this project, so what was sold is not recorded here.",
        },
      ],
      deals: [],
      stakeholders: [],
      open_commitments: [],
    },
  },
};

/** A payload that is not a handoff is refused rather than cleared for
 *  handover. */
export const WrongShape: Story = { args: { data: 42 } };
