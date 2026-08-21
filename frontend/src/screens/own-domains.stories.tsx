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
  // What the COMPANY PROFILE claims, which this card reads and cannot change.
  // Defaulted to none because most stories are about the curated half; the one
  // story below that passes them is the only place the card's two-owner shape
  // is visible at all.
  anchors: string[] = [],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/email-domains": () =>
        jsonResponse({ data: domains, anchor_domains: anchors }),
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
  title: "Settings/Admin settings/Capture/Own domains",
  component: OwnDomainsCard,
};
export default meta;
type Story = StoryObj<typeof OwnDomainsCard>;

export const Populated: Story = {
  render: story([ADMIN_ENTERED, OBSERVED], MANAGER),
};

export const Empty: Story = { render: story([], MANAGER) };

// Both halves of the card, which is the state it exists for: the domains the
// company profile claims and the ones curated here, each a row per domain in the
// one row language, and only the curated half carrying verbs. What to read here
// is whether the two groups stay distinguishable now that their entries speak
// the same row shape — the naming, its link, and the ownership it implies are
// all that separate them.
export const WithCompanyClaimedDomains: Story = {
  render: story([ADMIN_ENTERED, OBSERVED], MANAGER, [
    "brandt-automotive.de",
    "brandt-group.example",
  ]),
};

// Readable, unwritable: the rows stay, the add row and the per-row verbs go, and
// one sentence says why rather than twelve disabled controls.
export const ReadOnly: Story = {
  render: story([ADMIN_ENTERED, OBSERVED], READER),
};

// The rows at 390px, and the reason this is the story worth having rather than a
// dark one: both flex rows on this card were written without `flex-wrap`. A
// domain row is `space-between` with the name plus its confirmed/candidate label
// on one side and a ghost Remove on the other; the add row is a TextInput and a
// primary Add. Neither may wrap, and a Button never wraps its own label
// (base.css `.btn` is nowrap) — so at a phone the name is the only thing that can
// give, and what to check is whether "brandt-automotive.de · Confirmed" survives
// beside a verb or gets squeezed into a two-character column.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const PopulatedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story([ADMIN_ENTERED, OBSERVED], MANAGER),
};
