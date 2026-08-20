// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MailSharingCard } from "./mail-sharing";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The workspace mail-sharing posture. ON is the quiet default; OFF is loud on
// purpose — the danger callout under the switch is the state worth a picture,
// because it is the one screen that says out loud what turning sharing off
// costs.
function story(mail_sharing: boolean, allow: Parameters<typeof meRoute>[0]) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/settings": () =>
        jsonResponse({ auto_enrich: true, mail_sharing }),
    });
    return (
      <StoryProviders>
        <MailSharingCard />
      </StoryProviders>
    );
  };
}

const MANAGER = { capture_settings: ["read", "update"] } as const;
const READER = { capture_settings: ["read"] } as const;

const meta: Meta<typeof MailSharingCard> = {
  title: "Settings/You/Connections/Email sharing",
  component: MailSharingCard,
};
export default meta;
type Story = StoryObj<typeof MailSharingCard>;

export const SharingOn: Story = { render: story(true, MANAGER) };

// The stored posture IS off: the danger callout stands without any
// interaction, so an admin landing here later still sees the cost stated.
export const SharingOff: Story = { render: story(false, MANAGER) };

export const CannotChange: Story = { render: story(true, READER) };
