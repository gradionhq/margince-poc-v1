import type { Meta, StoryObj } from "@storybook/react-vite";
import { AiUsageCard } from "./aiusage";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

function story(band: string, tasks: Record<string, unknown>[]) {
  return () => {
    installFetchStub({
      "GET /ai/usage": () =>
        jsonResponse({
          days: tasks.length ? [{ date: "2026-07-20", tasks }] : [],
          budget: {
            monthly_tokens: 1000,
            spent_tokens:
              band === "queued" ? 1000 : band === "degraded" ? 850 : 200,
            band,
            currency: "EUR",
          },
        }),
    });
    return (
      <StoryProviders>
        <AiUsageCard />
      </StoryProviders>
    );
  };
}

const task = {
  task: "capture_classify",
  tier: "cheap_cloud",
  calls: 8,
  cached_hits: 2,
  tokens_in: 1200,
  tokens_out: 240,
};
const meta: Meta<typeof AiUsageCard> = {
  title: "screens/ai-usage",
  component: AiUsageCard,
};
export default meta;
type Story = StoryObj<typeof AiUsageCard>;
export const Normal: Story = { render: story("normal", [task]) };
export const EconomyMode: Story = { render: story("degraded", [task]) };
export const Queued: Story = { render: story("queued", [task]) };
export const WithCost: Story = {
  render: story("normal", [{ ...task, cost_est_minor: 124 }]),
};
export const Empty: Story = { render: story("normal", []) };
