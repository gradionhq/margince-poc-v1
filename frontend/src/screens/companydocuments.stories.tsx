// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyDocumentsCard } from "./companydocuments";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The account's document library. Two rules the card is built around, both
// invisible on a well-behaved fixture: `doc_state` is never inferred from
// upload order, and a file that cannot be downloaded (scanning, blocked) is
// still LISTED rather than hidden — so the story below carries at least one
// row in each of those states, not just the clean happy path.

const meta: Meta = {
  title: "Screens/Company documents",
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
    scan_status: "clean",
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
    scan_status: "scanning",
    created_at: "2026-08-02T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
  {
    id: "d-3",
    filename: "blocked.pdf",
    category: "other",
    doc_state: "current",
    pinned: false,
    scan_status: "blocked",
    created_at: "2026-08-03T09:00:00Z",
    entity_type: "organization",
    entity_id: "o-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
];

function Documents({ data }: Readonly<{ data: Attachment[] }>) {
  installFetchStub({
    "GET /organizations/o-1/documents": () => jsonResponse({ data, page }),
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

// An unfiltered zero is the account's own emptiness — the only case the card
// says "no documents" rather than "no matches in this category".
export const Empty: Story = { render: () => <Documents data={[]} /> };
