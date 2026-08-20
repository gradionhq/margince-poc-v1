// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ConsumerMailDomainsCard } from "./consumer-mail-domains";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Mail from a consumer domain still creates the person; what it never creates is
// a company. The shipped baseline is ~8 700 domains, so this card is where an
// operator adds what it missed (`extra`) and takes back what it wrongly claimed
// (`never`).
const ADDED = { id: "cm-1", domain: "gmx.at", kind: "extra" };
const CARVED_OUT = { id: "cm-2", domain: "brandt-partner.de", kind: "never" };

function story(
  entries: Record<string, unknown>[],
  allow: Parameters<typeof meRoute>[0],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/consumer-mail-domains": () =>
        jsonResponse({ data: entries }),
      "GET /capture/consumer-mail-baseline": () =>
        jsonResponse({ data: [], total: 8_700, matched: 0 }),
    });
    return (
      <StoryProviders>
        <ConsumerMailDomainsCard />
      </StoryProviders>
    );
  };
}

// The server splits the write: any seat with create contributes an `extra`,
// while a `never` carve-out and removal need update.
const OPS = { capture_settings: ["read", "create", "update"] } as const;
const CONTRIBUTOR = { capture_settings: ["read", "create"] } as const;
const READER = { capture_settings: ["read"] } as const;

const meta: Meta<typeof ConsumerMailDomainsCard> = {
  title: "Settings/Admin settings/Capture/Consumer mailboxes",
  component: ConsumerMailDomainsCard,
};
export default meta;
type Story = StoryObj<typeof ConsumerMailDomainsCard>;

export const Populated: Story = {
  render: story([ADDED, CARVED_OUT], OPS),
};

export const Empty: Story = { render: story([], OPS) };

// Create without update: the seat may add a domain the baseline missed and may
// not carve one out or remove one. A single "can you manage this" flag could not
// express it, and the card's controls disable rather than vanish.
export const CanAddButNotCarveOut: Story = {
  render: story([ADDED, CARVED_OUT], CONTRIBUTOR),
};

export const ReadOnly: Story = {
  render: story([ADDED, CARVED_OUT], READER),
};

// The card at 390px. An entry row keeps four things on one line — icon, domain,
// what the domain IS, and the remove button — and the domain is the only one of
// them that can be long. Watching whether the row gives it the width or breaks a
// hostname mid-token to keep the sentence beside it.
export const PopulatedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([ADDED, CARVED_OUT], OPS),
};
