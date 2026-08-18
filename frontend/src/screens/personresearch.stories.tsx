import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { providerCompletedProfile } from "./personprovider.fixtures";
import { PersonResearchTab } from "./personresearch";
import { StoryProviders } from "./story-utils";

// The Research tab's own gallery: the two halves it stacks (the bought
// provider snapshot and the enrichment evidence sidecar), the tab-wide empty
// state that collapses them into one line rather than two blank panels, and
// the provider half withheld for lack of a grant.

const meta: Meta<typeof PersonResearchTab> = {
  title: "Records/Person record/Research tab",
  component: PersonResearchTab,
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj<typeof PersonResearchTab>;
type View = components["schemas"]["Person360"];

const person: View["person"] = {
  id: "p-1",
  full_name: "Dana Buyer",
  first_name: "Dana",
  last_name: "Buyer",
  owner_id: "u-1",
  source: "ui",
  captured_by: "human:u-1",
  created_at: "2026-08-01T09:00:00Z",
  updated_at: "2026-08-12T09:00:00Z",
};

const profileFields: components["schemas"]["PersonProfileField"][] = [
  {
    field: "title",
    value: "Head of Fleet",
    evidence_snippet: "Dana Buyer, Head of Fleet at Brandt Automotive GmbH",
    source_ref: "site_read:https://brandt-automotive.example/team",
    confidence: 0.91,
    source: "site_read",
    captured_by: "agent:enrich",
    captured_at: "2026-08-10T09:00:00Z",
    claim_key: "profile_field:title",
  },
  {
    field: "phone",
    value: "+493012345678",
    evidence_snippet: "Reach Dana directly at +49 30 12345678.",
    confidence: 0.6,
    source: "capture_enrich",
    captured_by: "human:u-2",
    captured_at: "2026-08-11T09:00:00Z",
    claim_key: "profile_field:phone",
    verdict: "confirmed",
  },
];

const populated: View = {
  as_of: "2026-08-13T09:00:00Z",
  person,
  sections_omitted: [],
  provider_profile: providerCompletedProfile,
  profile_fields: profileFields,
};

/** Both halves populated: a bought snapshot and enrichment evidence with a
 *  provenance mark per value. */
export const Populated: Story = {
  render: () => (
    <StoryProviders>
      <PersonResearchTab view={populated} />
    </StoryProviders>
  ),
};

const empty: View = {
  as_of: "2026-08-13T09:00:00Z",
  person,
  sections_omitted: [],
  profile_fields: [],
};

/** Neither half has anything to show: one empty line, not two blank panels. */
export const Empty: Story = {
  render: () => (
    <StoryProviders>
      <PersonResearchTab view={empty} />
    </StoryProviders>
  ),
};

const providerWithheld: View = {
  as_of: "2026-08-13T09:00:00Z",
  person,
  sections_omitted: ["provider_profile"],
  profile_fields: profileFields,
};

/** The provider half withheld for lack of a grant, beside a fields panel that
 *  still has evidence to show — withheld and empty are different facts, and
 *  only the provider half is the withheld one here. */
export const ProviderWithheld: Story = {
  render: () => (
    <StoryProviders>
      <PersonResearchTab view={providerWithheld} />
    </StoryProviders>
  ),
};
