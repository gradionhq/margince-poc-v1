import type { Meta, StoryObj } from "@storybook/react-vite";
import { AuthScreen, AvailabilityScreen } from "./auth";
import type { AssistantProfile } from "./auth-core";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

/**
 * The unauthenticated surface, whole (ADR-0076).
 *
 * The Core is a design-system primitive now, so its own states live in
 * `Design System/Margince Core`; these stories are about the SURFACE — the two
 * regions, the runtime posture the identity region reads from the server, and
 * the states that are not a failed password.
 *
 * Not here, and deliberately: the credential error, the rate limit and the
 * pending button. They need a submit to happen, so they are covered where they
 * can be asserted rather than eyeballed (`auth.test.tsx`), and visually in the
 * interactive mockup's catalog where they were designed.
 */
const meta: Meta = {
  title: "Screens/Auth/Unauthenticated surface",
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
};

function AuthStory({
  profile,
  profileStatus = 200,
  notice,
  // Empty is what the running installation serves — the OIDC flow has not
  // shipped (§19) — so it stays the default here too. A STORY seeds providers to
  // review the federated design; nothing under src/ ever does.
  oidcProviders = [],
}: Readonly<{
  profile: AssistantProfile;
  profileStatus?: number;
  notice?: "session-expired" | "signed-out";
  oidcProviders?: ReadonlyArray<{ key: string; label: string }>;
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
        oidc_providers: oidcProviders,
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

/**
 * The installation's administrator has wired SSO (§11).
 *
 * This is the ONLY place in this repo the federated block can be seen: the real
 * server serves `oidc_providers: []` until the OIDC flow ships, and the component
 * renders nothing for an empty list. Seeding it here reviews the design without
 * claiming the build can complete the flow.
 *
 * The password form is still there and still complete — it is the fallback door,
 * which is why the divider labels IT rather than the buttons above.
 */
export const WithProviders: Story = {
  render: () => (
    <AuthStory
      profile={configured}
      oidcProviders={[
        { key: "google", label: "Continue with Google" },
        { key: "microsoft", label: "Continue with Microsoft" },
      ]}
    />
  ),
};

/**
 * A provider this frontend has never heard of, which is the normal case for a
 * self-hosted product: `oidc_providers[].key` is an open string in the contract.
 *
 * Two things are load-bearing here. The label is the INSTALLATION's own text, so
 * it can be German while the rest of the catalog is English. And the mark falls
 * back to a neutral key icon rather than the block disappearing, because a
 * working sign-in path must not be hidden for want of a logo.
 */
export const UnknownProvider: Story = {
  render: () => (
    <AuthStory
      profile={configured}
      oidcProviders={[{ key: "corp-sso", label: "Anmeldung über Werk-IT" }]}
    />
  ),
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

/**
 * §4: a server outage is not a wrong password. Same two-region frame, different
 * product state, and the Core reads `unavailable` — the one state that does not
 * breathe.
 */
export const ConnectionProblem: Story = {
  render: () => (
    <StoryProviders>
      <AvailabilityScreen kind="connection" onRetry={() => undefined} />
    </StoryProviders>
  ),
};

export const InstallationUnavailable: Story = {
  render: () => (
    <StoryProviders>
      <AvailabilityScreen kind="installation" onRetry={() => undefined} />
    </StoryProviders>
  ),
};

export const SignedOut: Story = {
  render: () => <AuthStory profile={configured} notice="signed-out" />,
};
