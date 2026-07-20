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
  title: "screens/ai-export",
  component: ExportScenarioDialog,
};
export default meta;
type Story = StoryObj<typeof ExportScenarioDialog>;
export const Dialog: Story = {
  render: () => (
    <StoryProviders>
      <ExportScenarioDialog call={call} onClose={() => {}} />
    </StoryProviders>
  ),
};
