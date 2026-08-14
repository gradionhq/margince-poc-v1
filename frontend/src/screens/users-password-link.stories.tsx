// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { PasswordLinkModal } from "./users-password-link";

// The one-time link an admin hands a member who cannot sign in. Prop-driven, so
// the three states it can be in are three sets of props rather than three
// server fixtures — and all three matter: the link is shown once, so the
// pending and failed states are what a reader sees when it is not.
const LINK = {
  url: "https://margince.example/set-password/9f2c1a7e",
  expiresAt: "2026-08-15T09:00:00Z",
};

const meta: Meta<typeof PasswordLinkModal> = {
  title: "Settings/Organization/People and access/Password link",
  component: PasswordLinkModal,
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj<typeof PasswordLinkModal>;

export const Minted: Story = {
  render: () => (
    <PasswordLinkModal
      onClose={() => undefined}
      memberName="Dana Kessler"
      link={LINK}
      pending={false}
      error={null}
      onRetry={() => undefined}
    />
  ),
};

export const Minting: Story = {
  render: () => (
    <PasswordLinkModal
      onClose={() => undefined}
      memberName="Dana Kessler"
      link={null}
      pending
      error={null}
      onRetry={() => undefined}
    />
  ),
};

export const Failed: Story = {
  render: () => (
    <PasswordLinkModal
      onClose={() => undefined}
      memberName="Dana Kessler"
      link={null}
      pending={false}
      error="The link could not be minted. Try again."
      onRetry={() => undefined}
    />
  ),
};
