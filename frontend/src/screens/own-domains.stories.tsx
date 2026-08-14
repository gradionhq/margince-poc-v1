// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { OwnDomainsCard } from "./own-domains";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Which domains are OURS — the posture the two capture judgements below it read.
const ADMIN_ENTERED = {
  domain: "brandt-automotive.de",
  source: "admin",
  verified: true,
  first_seen_at: "2026-06-01T08:00:00Z",
};

// Observed from a connected mailbox and not yet vouched for: a candidate, which
// is the row a reader is here to act on.
const OBSERVED = {
  domain: "brandt-fleet.example",
  source: "mailbox",
  verified: false,
  first_seen_at: "2026-07-22T11:30:00Z",
};

function story(
  domains: Record<string, unknown>[],
  allow: Parameters<typeof meRoute>[0],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/email-domains": () => jsonResponse({ data: domains }),
    });
    return (
      <StoryProviders>
        <OwnDomainsCard />
      </StoryProviders>
    );
  };
}

const MANAGER = { capture_settings: ["read", "update"] } as const;
const READER = { capture_settings: ["read"] } as const;

const meta: Meta<typeof OwnDomainsCard> = {
  title: "Settings/Organization/Capture/Own domains",
  component: OwnDomainsCard,
};
export default meta;
type Story = StoryObj<typeof OwnDomainsCard>;

export const Populated: Story = {
  render: story([ADMIN_ENTERED, OBSERVED], MANAGER),
};

export const Empty: Story = { render: story([], MANAGER) };

// Readable, unwritable: the rows stay, the add row and the per-row verbs go, and
// one sentence says why rather than twelve disabled controls.
export const ReadOnly: Story = {
  render: story([ADMIN_ENTERED, OBSERVED], READER),
};
