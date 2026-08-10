import type { Meta, StoryObj } from "@storybook/react-vite";
import { ViewHost } from "../view-host";
import { relationshipMapFixture } from "./fixture";
import { render } from "./main";

const meta: Meta<typeof ViewHost> = {
  title: "MCP Apps/Relationship map",
  component: ViewHost,
  args: { render },
};
export default meta;

type Story = StoryObj<typeof ViewHost>;

export const Populated: Story = { args: { data: relationshipMapFixture.data } };

/** Nobody having spoken to the contact is the answer, not a gap. */
export const Empty: Story = {
  args: {
    data: { person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7", colleagues: [] },
  },
};

/** A capped sweep is not the whole network, and the meta line stops claiming
 *  "warmest first" the moment the read stopped at its bound. */
export const Truncated: Story = {
  args: {
    data: relationshipMapFixture.data,
    warnings: [{ code: "sweep_truncated" }],
  },
};

/** A band the seam has not published yet renders with no colour rather than the
 *  wrong one. */
export const UnknownBand: Story = {
  args: {
    data: {
      person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
      colleagues: [
        {
          display_name: "Sam Ferrier",
          strength_bucket: "scorching",
          interactions_90d: 3,
        },
      ],
    },
  },
};

export const WrongShape: Story = { args: { data: 42 } };
