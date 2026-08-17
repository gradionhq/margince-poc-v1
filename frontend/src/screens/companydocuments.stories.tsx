// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import type { GrantSpec } from "../app/mefixture";
import { CompanyDocumentsCard } from "./companydocuments";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The account's document library. The rule the card is built around is
// invisible on a well-behaved fixture: `doc_state` is asserted, never inferred
// from upload order — so the fixture below deliberately lists its DRAFT after
// the final one, which is the case an upload-date guess would get wrong.

const meta: Meta = {
  title: "Records/Company 360/Documents",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Attachment = components["schemas"]["Attachment"];

const page = { has_more: false, next_cursor: null };

const documents: Attachment[] = [
  {
    id: "d-1",
    filename: "Rahmenvertrag.pdf",
    title: "Framework agreement — signed",
    category: "contract",
    doc_state: "final",
    pinned: true,
    created_at: "2026-08-01T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
  {
    id: "d-2",
    filename: "scan_0001.pdf",
    category: "other",
    doc_state: "draft",
    pinned: false,
    created_at: "2026-08-02T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
  {
    id: "d-3",
    filename: "Kuendigung.pdf",
    title: "Notice of termination",
    category: "legal",
    doc_state: "current",
    pinned: false,
    created_at: "2026-08-03T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
];

const deal = {
  id: "deal-1",
  name: "Pallet Handling Programme — Graz",
  organization_id: "o-1",
  status: "open",
};

const dealDocument = {
  id: "d-4",
  filename: "order_form.txt",
  category: "other",
  doc_state: "current",
  pinned: false,
  created_at: "2026-08-17T09:00:00Z",
  entity_type: "deal",
  entity_id: "deal-1",
  source: "upload",
  captured_by: "human:u-1",
} as unknown as Attachment;

function Documents({
  data,
  allow = { deal: ["update"], organization: ["update"] },
  seat = "full",
}: Readonly<{
  data: Attachment[];
  allow?: GrantSpec;
  seat?: "full" | "read";
}>) {
  installFetchStub({
    "GET /organizations/o-1/documents": () => jsonResponse({ data, page }),
    // Spelled out rather than defaulted: the Accept control on a deal-scoped
    // reading and the Add a document button are both grant-gated, so a story
    // that left /me unrouted would draw the refused branch it is not named for.
    "GET /me": meRoute(allow, { seat }),
    "GET /deals": () => jsonResponse({ data: [deal], page }),
    // 404 is the honest "nobody has read this file yet", which is the state a
    // freshly added document is in and the one the offer to read is drawn
    // from — not a failure, and not something the panel can invent.
    "GET /attachments/d-4/extraction": () =>
      jsonResponse({ title: "Not Found", status: 404 }, 404),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 640 }}>
        <CompanyDocumentsCard orgId="o-1" />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = {
  render: () => <Documents data={documents} />,
};

// A file hanging off a DEAL, which is the only kind the extraction panel offers
// to read — and, before the Add a document dialog, the only kind no control in
// the product could create.
export const OnADeal: Story = {
  render: () => <Documents data={[dealDocument]} />,
};

// A read seat holding the very same grants: it may open every document listed
// and write none of them. The seat is the only axis that differs from
// `Populated`, which is the point — the server clamps it on the HTTP method,
// before RBAC ever runs.
export const ReadOnlySeat: Story = {
  render: () => <Documents data={documents} seat="read" />,
};

// An unfiltered zero is the account's own emptiness — the only case the card
// says "no documents" rather than "no matches in this category".
export const Empty: Story = { render: () => <Documents data={[]} /> };
