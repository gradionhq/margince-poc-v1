import type { Meta, StoryObj } from "@storybook/react-vite";
import { SettingsScreen } from "./settings";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Screens/Settings",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

function installSettingsStub() {
  installFetchStub(
    {
      "GET /me": () =>
        jsonResponse({
          user: { email: "ada@acme.test" },
          roles: ["admin"],
          teams: [],
        }),
    },
    () => jsonResponse(emptyPage),
  );
}

export const Default: Story = {
  render: () => {
    installSettingsStub();
    return (
      <StoryProviders>
        <SettingsScreen />
      </StoryProviders>
    );
  },
};
