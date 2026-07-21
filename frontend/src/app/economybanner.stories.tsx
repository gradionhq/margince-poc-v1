import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { EconomyBanner } from "./economybanner";

function story(band: string) {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          user: {
            id: "u1",
            email: "admin@example.test",
            display_name: "Admin",
          },
          roles: ["admin"],
        }),
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
  title: "app/economy-banner",
  component: EconomyBanner,
};
export default meta;
type Story = StoryObj<typeof EconomyBanner>;
export const Degraded: Story = { render: story("degraded") };
export const Queued: Story = { render: story("queued") };
