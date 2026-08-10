import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { EconomyBanner } from "./economybanner";
import { meFixture } from "./mefixture";

function story(band: string) {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { automation: ["update"] } })),
      "GET /ai/usage": () =>
        jsonResponse({
          days: [],
          budget: { monthly_tokens: 100, spent_tokens: 85, band },
        }),
    });
    return (
      <StoryProviders>
        <EconomyBanner />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof EconomyBanner> = {
  title: "App/economy-banner",
  component: EconomyBanner,
};
export default meta;
type Story = StoryObj<typeof EconomyBanner>;
export const Degraded: Story = { render: story("degraded") };
export const Queued: Story = { render: story("queued") };
