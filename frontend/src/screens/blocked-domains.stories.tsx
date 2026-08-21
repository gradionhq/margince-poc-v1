// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { BlockedDomainsCard } from "./blocked-domains";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The three sources side by side, because telling them apart is the card's whole
// job: a model verdict, a heuristic, and a person who deliberately let a domain
// back in. The admitted row carries a company id — the McKinsey case, where
// unblocking re-asked the company question and one landed.
const BY_VERDICT = {
  domain: "substack.example",
  admission: "suppressed",
  reason: "a newsletter platform, not a customer",
  source: "verdict",
  decided_at: "2026-07-30T09:12:00Z",
  organization_id: null,
};
const BY_HEURISTIC = {
  domain: "expensify.example",
  admission: "suppressed",
  reason: "bulk sender: no reply address, list-unsubscribe header",
  source: "heuristic",
  decided_at: "2026-08-02T14:40:00Z",
  organization_id: null,
};
const BY_HUMAN = {
  domain: "mckinsey.example",
  admission: "admitted",
  reason: "they became a client in July",
  source: "human",
  decided_at: "2026-08-11T07:05:00Z",
  organization_id: "018f3a1b-0000-7000-8000-00000000c001",
};

function story(
  entries: Record<string, unknown>[],
  total: number,
  allow: Parameters<typeof meRoute>[0],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/blocked-domains": () =>
        jsonResponse({ data: entries, total }),
      "PUT /capture/blocked-domains": (body) => jsonResponse(body),
    });
    return (
      <StoryProviders>
        <BlockedDomainsCard />
      </StoryProviders>
    );
  };
}

// Reading is every human role's; changing an entry is organization:update.
const OPS = { organization: ["read", "update"] } as const;
const READER = { organization: ["read"] } as const;

const meta: Meta<typeof BlockedDomainsCard> = {
  title: "Settings/Admin settings/Capture/Refused domains",
  component: BlockedDomainsCard,
};
export default meta;
type Story = StoryObj<typeof BlockedDomainsCard>;

export const Populated: Story = {
  render: story([BY_VERDICT, BY_HEURISTIC, BY_HUMAN], 3, OPS),
};

// Nothing refused yet. It has to read as a fact about the installation rather
// than as a table that failed to draw.
export const Empty: Story = { render: story([], 0, OPS) };

// Refusals accumulate on their own from every bulk-sender verdict, so the list
// is paged at 200 and the response says how many exist. The count line under the
// table is the only thing that stops "past the end of this page" reading as
// "never refused".
export const PastTheFirstPage: Story = {
  render: story([BY_VERDICT, BY_HEURISTIC, BY_HUMAN], 412, OPS),
};

// A seat that may read the posture and not change it: the controls disable and
// say why, rather than vanishing and leaving the reader to guess whether the
// list is complete.
export const ReadOnly: Story = {
  render: story([BY_VERDICT, BY_HEURISTIC, BY_HUMAN], 3, READER),
};

// The card at 390px. A reason is a whole sentence and a domain is one unbroken
// token, so this is where the table decides which of them gets the width.
export const PopulatedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([BY_VERDICT, BY_HEURISTIC, BY_HUMAN], 3, OPS),
};
