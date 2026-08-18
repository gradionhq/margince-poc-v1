// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { PersonFilesTab } from "./personfiles";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The person's own file library — its own fetch, distinct from the 360's
// composite read, so its own set of stories: rows present, an unfiltered
// empty library, and a read that failed and can be retried.

const meta: Meta = {
  title: "Records/Person record/Files tab",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Attachment = components["schemas"]["Attachment"];

const page = { has_more: false, next_cursor: null };

const files: Attachment[] = [
  {
    id: "f-1",
    filename: "Lebenslauf.pdf",
    title: "CV — updated",
    category: "other",
    created_at: "2026-08-01T09:00:00Z",
    entity_type: "person",
    entity_id: "p-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
  {
    id: "f-2",
    filename: "nda_signed.pdf",
    category: "legal",
    created_at: "2026-08-05T09:00:00Z",
    entity_type: "person",
    entity_id: "p-1",
    source: "upload",
    captured_by: "human:u-1",
  } as unknown as Attachment,
];

function Files({ data }: Readonly<{ data: Attachment[] }>) {
  installFetchStub({
    "GET /attachments": () => jsonResponse({ data, page }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 640 }}>
        <PersonFilesTab personId="p-1" />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = {
  render: () => <Files data={files} />,
};

// An unfiltered zero is this person's own emptiness — the tab has no filter
// to clear, so an empty page is always this state.
export const Empty: Story = { render: () => <Files data={[]} /> };

export const Failed: Story = {
  render: () => {
    installFetchStub({
      "GET /attachments": () =>
        jsonResponse({ title: "Error", status: 500 }, 500),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 640 }}>
          <PersonFilesTab personId="p-1" />
        </div>
      </StoryProviders>
    );
  },
};
