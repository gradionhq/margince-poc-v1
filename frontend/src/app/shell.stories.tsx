import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { Shell } from "./shell";

const meta: Meta<typeof Shell> = { title: "app/shell", component: Shell };
export default meta;
type Story = StoryObj<typeof Shell>;
export const Default: Story = {
  render: () => {
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
          budget: { monthly_tokens: 100, spent_tokens: 20, band: "normal" },
        }),
    });
    return (
      <StoryProviders>
        <Shell onOpenSearch={() => {}}>
          <div className="card">Content</div>
        </Shell>
      </StoryProviders>
    );
  },
};
