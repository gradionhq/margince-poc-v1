// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { TagsSection } from "./companyrailtags";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// TagsSection carries two independently-governed halves — lists and tags —
// each stating its own withheld/empty/ready rather than one verdict speaking
// for both. The withheld story below is the only place a reader sees BOTH
// halves say so at once, and with them the add-tag/add-to-list verbs gone:
// each verb renders only once its own half has answered.

const meta: Meta = {
  title: "Records/Company rail/Tags",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const ready = {
  as_of: "2026-06-01T09:00:00Z",
  organization: org,
  sections_omitted: [],
  tags: [
    { id: "t-1", workspace_id: "w-1", name: "Key account" },
    { id: "t-2", workspace_id: "w-1", name: "Renewal Q3" },
  ],
  list_memberships: [
    {
      id: "l-1",
      workspace_id: "w-1",
      name: "Q3 renewals",
      entity_type: "organization",
      list_type: "static",
    },
  ],
} as unknown as View;

// Both halves withheld — the shape a role scoped away from tags AND lists
// reads on any real workspace: no count badge, a restricted notice in place
// of each list, and neither add-verb offered.
const withheld = {
  ...ready,
  tags: undefined,
  list_memberships: undefined,
  sections_omitted: ["tags", "list_memberships"],
} as unknown as View;

function Tags({ view }: Readonly<{ view: View }>) {
  installFetchStub({
    "GET /me": () =>
      jsonResponse({
        user: { id: "u-1", display_name: "Mira Voss" },
        authorization: { objects: { organization: { update: true } } },
      }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 340 }}>
        <TagsSection view={view} orgId="o-1" loading={false} />
      </div>
    </StoryProviders>
  );
}

export const Ready: Story = { render: () => <Tags view={ready} /> };

export const Withheld: Story = { render: () => <Tags view={withheld} /> };
