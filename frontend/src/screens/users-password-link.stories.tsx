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
  title: "Settings/Admin settings/People and access/Password link",
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

// The narrow case this dialog was built for, and the only one worth a variant:
// `.users-link-row` claims it "wraps to two lines on a narrow card rather than
// overflowing it", with the URL input at `flex: 1 1 20rem` beside a Copy button.
// 20rem does not fit a 390px phone, so the wrap is load-bearing — and the link is
// a live account-takeover credential the admin has to read off the screen to
// dictate, so a URL clipped by an overflowing row is the failure that matters
// here. The modal is `size="wide"`, which is the other half of the question.
export const MintedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
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
