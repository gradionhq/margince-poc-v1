// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CaptureExclusionsCard } from "./capture-exclusions";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Pre-capture exclusions: the addresses and domains whose mail never enters the
// CRM. Two scopes live on one card, and telling them apart is the whole point —
// a rule the reader made for their own mailboxes is theirs to take back, and one
// that binds everybody is admin/ops work. So both stories carry both scopes.
const MINE = {
  id: "ex-1",
  scope: "user",
  kind: "address",
  value: "coach@personal.example",
};
const EVERYONE = {
  id: "ex-2",
  scope: "workspace",
  kind: "domain",
  value: "recruiting.example",
};

function story(
  rules: Record<string, unknown>[],
  allow: Parameters<typeof meRoute>[0],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/exclusions": () => jsonResponse({ data: rules }),
    });
    return (
      <StoryProviders>
        <CaptureExclusionsCard />
      </StoryProviders>
    );
  };
}

// Excluding one of your OWN correspondents needs no grant; the organization-wide
// rule is what capture_settings:update buys.
const OPS = { capture_settings: ["read", "update"] } as const;
const READER = { capture_settings: ["read"] } as const;

const meta: Meta<typeof CaptureExclusionsCard> = {
  title: "Settings/Admin settings/Capture/Keep out of capture",
  component: CaptureExclusionsCard,
};
export default meta;
type Story = StoryObj<typeof CaptureExclusionsCard>;

export const Populated: Story = { render: story([MINE, EVERYONE], OPS) };

// Nothing excluded. It has to read as a fact about the installation rather than
// as a list that failed to draw — and left-aligned at a row's interval, not as a
// grey slab in the middle of the card.
export const Empty: Story = { render: story([], OPS) };

// The state the seeded demo cannot show: a seat that may keep its own
// correspondent out and may not touch the rule binding everyone. The
// organization row's verb refuses and points at the one sentence under the list;
// the reader's own row stays live beside it. That per-ROW split is what this card
// has and the other capture cards do not.
export const OrganizationRuleRefused: Story = {
  render: story([MINE, EVERYONE], READER),
};

export const OrganizationRuleRefusedDark: Story = {
  globals: { theme: "dark" },
  render: story([MINE, EVERYONE], READER),
};

// The rows at 390px. An excluded value is one unbroken token — an address or a
// domain — and its scope and kind sit beside it as the row's answer, so this is
// where the row decides which of the two gives up width first.
export const PopulatedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([MINE, EVERYONE], OPS),
};
