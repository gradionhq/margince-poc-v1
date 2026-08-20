// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { RestrictedRecordsCard } from "./restrictedrecords";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Settings → Privacy → Restricted records. Two states worth reviewing: a
// record held under the statutory floor, named by its transactions and its
// deadline; and the empty card, which says every erasure completed in full.

const HELD = {
  activity_id: "00000000-0000-4000-8000-0000000000b1",
  kind: "email",
  occurred_at: "2025-03-04T09:00:00Z",
  restricted_at: "2026-08-18T07:00:00Z",
  restricted_until: "2032-01-01T00:00:00Z",
  reason: "commercial_correspondence · §257 HGB / §147 AO",
  deals: [{ id: "00000000-0000-4000-8000-0000000000d1", name: "Acme rollout" }],
  redacted_fields: ["raw", "counterparty_email"],
};

function restricted(records: unknown[], decide = false) {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(
          meFixture({
            allow: {
              retention_policy: decide ? ["read", "update"] : ["read"],
            },
          }),
        ),
      "GET /retention/restrictions": () =>
        jsonResponse({
          data: records,
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <RestrictedRecordsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof RestrictedRecordsCard> = {
  title: "Settings/Admin settings/Privacy/RestrictedRecords",
  component: RestrictedRecordsCard,
};
export default meta;

type Story = StoryObj<typeof RestrictedRecordsCard>;

export const Held: Story = { render: restricted([HELD]) };
export const NothingHeld: Story = { render: restricted([]) };
export const HeldDark: Story = {
  globals: { theme: "dark" },
  render: restricted([HELD]),
};

// The controller's own view: the release action on the row and the pin form
// above it. Both are irreversible and both demand a typed reason, which is
// what the confirm dialog behind them is for.
export const WithTheRetentionAuthority: Story = {
  render: restricted([HELD], true),
};
