import type { Meta, StoryObj } from "@storybook/react-vite";
import { AuthScreen } from "./auth";
import type { AssistantProfile } from "./auth-core";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const meta: Meta = {
  title: "Screens/Auth/Margince Core",
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj;

const configured: AssistantProfile = {
  name: "Margince",
  kind: "ai",
  state: "configured",
  inference_mode: "hybrid",
  providers: ["anthropic", "ollama"],
  configured_models: [
    { tier: "local_small", provider: "ollama", model: "gemma3" },
    { tier: "premium", provider: "anthropic", model: "claude-sonnet" },
  ],
};

function AuthStory({
  profile,
  profileStatus = 200,
  notice,
}: Readonly<{
  profile: AssistantProfile;
  profileStatus?: number;
  notice?: "session-expired" | "signed-out";
}>) {
  installFetchStub({
    "GET /assistant/profile": () =>
      jsonResponse(
        profileStatus === 200 ? profile : { title: "Unavailable" },
        profileStatus,
      ),
    "GET /auth/capabilities": () =>
      jsonResponse({
        password: true,
        password_reset: true,
        oidc_providers: [],
      }),
  });
  return (
    <StoryProviders>
      <AuthScreen onAuthed={() => undefined} notice={notice} />
    </StoryProviders>
  );
}

export const ConfiguredHybrid: Story = {
  render: () => <AuthStory profile={configured} />,
};

export const Unconfigured: Story = {
  render: () => (
    <AuthStory
      profile={{
        name: "Margince",
        kind: "ai",
        state: "unconfigured",
        inference_mode: "none",
        providers: [],
        configured_models: [],
      }}
    />
  ),
};

export const Development: Story = {
  render: () => (
    <AuthStory
      profile={{
        name: "Margince",
        kind: "ai",
        state: "development",
        inference_mode: "development",
        providers: [],
        configured_models: [],
      }}
    />
  ),
};

export const ProfileUnavailable: Story = {
  render: () => <AuthStory profile={configured} profileStatus={500} />,
};

export const SessionExpired: Story = {
  render: () => <AuthStory profile={configured} notice="session-expired" />,
};
