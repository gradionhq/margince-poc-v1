// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyLastOffer } from "./companycommercial";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The account's commercial reading: the last offer put in front of it, named
// off its own leading open deal. `leadingDeal` and `offerAmount` carry their
// own unit-test coverage for the selection rule itself — this story exists to
// pin what the two refusals in that rule look like ON THE PAGE, since a
// truncated deals page draws nothing and a reader with no seeded demo
// account ever sees that state.

const meta: Meta = {
  title: "Records/Company 360/Last offer",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const deals = [
  {
    deal_id: "d-1",
    name: "Fleet pilot renewal",
    status: "open" as const,
    stalled: false,
    amount: { amount_minor: 1_200_000, currency: "EUR" },
  },
  {
    deal_id: "d-2",
    name: "Depot expansion",
    status: "open" as const,
    stalled: false,
    amount: { amount_minor: 4_500_000, currency: "EUR" },
  },
];

const offer = {
  id: "of-1",
  deal_id: "d-2",
  offer_number: "AN-2026-0142",
  revision: 1,
  status: "sent" as const,
  currency: "EUR",
  valid_until: "2026-09-01",
  net_minor: 3_781_500,
  tax_minor: 718_500,
  gross_minor: 4_500_000,
};

function LastOffer({ view }: Readonly<{ view: View }>) {
  installFetchStub({
    "GET /me": () =>
      jsonResponse({
        user: { id: "u-1", display_name: "Mira Voss" },
        authorization: { objects: { offer: { read: true } } },
      }),
    "GET /deals/d-2/offers": () => jsonResponse({ data: [offer], page }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 480 }}>
        <CompanyLastOffer view={view} />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = {
  render: () => (
    <LastOffer view={{ deals: { data: deals, page } } as unknown as View} />
  ),
};

// The truncated-page refusal: the 360 caps `deals.data`, and the largest deal
// on the account may be off the end of it — so the block names no deal at
// all rather than risk pointing at the wrong offer. `CompanyLastOffer`
// renders nothing for this view, which is the state itself; the caption
// beside it is the story's own label, not the component's output.
export const Truncated: Story = {
  render: () => (
    <div>
      <p className="t-caption">
        Deals page truncated — the block names no leading deal.
      </p>
      <LastOffer
        view={
          {
            deals: { data: deals, page: { has_more: true } },
          } as unknown as View
        }
      />
    </div>
  ),
};
