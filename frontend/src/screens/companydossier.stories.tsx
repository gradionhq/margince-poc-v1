// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { DossierPanel } from "./companydossier";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// What the company IS, read from its own recorded facts (see companydossier.tsx's
// own doc comment). Fixture mirrors companydossier.test.tsx's DESCRIBED — a
// COMPLETE OrganizationDossier, not a cast one, so a missing required field
// fails here rather than rendering an unlabelled heading.

const meta: Meta = {
  title: "Records/Company 360/Dossier",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Dossier = components["schemas"]["OrganizationDossier"];

const described: Dossier = {
  organization_id: "o-1",
  generated_at: "2026-08-08T09:00:00Z",
  generated_by: "deterministic",
  sections: [
    {
      kind: "summary",
      sentences: [
        {
          text: "What they offer: load-shifting software.",
          nature: "fact",
          evidence: [{ entity_type: "profile_field", entity_id: "p-1" }],
        },
      ],
    },
    {
      kind: "markets",
      sentences: [
        {
          text: "Ideal customer: energy-intensive manufacturers.",
          nature: "fact",
          evidence: [{ entity_type: "profile_field", entity_id: "p-2" }],
        },
      ],
    },
  ],
};

function Dossier({ body }: Readonly<{ body: unknown }>) {
  installFetchStub({
    "GET /organizations/o-1/dossier": () => jsonResponse(body),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 480 }}>
        <DossierPanel orgId="o-1" enabled />
      </div>
    </StoryProviders>
  );
}

export const Described: Story = { render: () => <Dossier body={described} /> };

// Said out loud beside the content, never instead of it — a stale dossier is
// more useful than none.
export const Stale: Story = {
  render: () => <Dossier body={{ ...described, needs_refresh: true }} />,
};

// A company nobody has described, distinct from one this build cannot read.
export const NothingRecorded: Story = {
  render: () => <Dossier body={{ ...described, sections: [] }} />,
};
