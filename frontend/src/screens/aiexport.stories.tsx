import type { Meta, StoryObj } from "@storybook/react-vite";
import { type AiCallDetail, ExportScenarioDialog } from "./aiexport";
import { StoryProviders } from "./story-utils";

const call = {
  task: "capture_classify",
  occurred_at: "2026-07-20T10:00:00Z",
  payload: {
    request: {
      system: "Classify safely",
      messages: [{ role: "user", content: "Example" }],
    },
    response: "commitment",
  },
} satisfies AiCallDetail;
const meta: Meta<typeof ExportScenarioDialog> = {
  title: "Settings/Admin settings/AI/Scenario export",
  component: ExportScenarioDialog,
};
export default meta;
type Story = StoryObj<typeof ExportScenarioDialog>;

const renderDialog = () => (
  <StoryProviders>
    <ExportScenarioDialog call={call} onClose={() => {}} />
  </StoryProviders>
);

export const Dialog: Story = { render: renderDialog };

// The dialog in dark. Almost everything here is a tinted Callout or a
// `pre.code-block`, and the block paints `--bgCard` INSIDE a dialog that is
// already a raised surface — two grounds that only stay distinguishable if both
// re-resolve. The callout is what tells the reader this YAML is about to leave
// the installation, so it is the one thing that may not go quiet.
export const DialogDark: Story = {
  globals: { theme: "dark" },
  render: renderDialog,
};

// At 390px the payload is the risk: prompt YAML has long unwrapped lines, and
// `.code-block` answers with `pre-wrap` plus its own scroll. Watching that the
// wrapping happens in the block rather than the dialog growing past the screen
// and taking the copy/close buttons with it.
export const DialogPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: renderDialog,
};
