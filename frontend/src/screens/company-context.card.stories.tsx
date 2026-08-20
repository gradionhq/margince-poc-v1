// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CompanyContextCard } from "./company-context";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// What the AI is told about the company it is selling for. Two independent
// conditions govern this card and they mean different things: a rollout FLAG
// says whether the surface exists on this installation at all, and the
// organization grant says whether this reader may change it.
const PROFILE = {
  display_name: "Brandt Automotive GmbH",
  legal_name: "Brandt Automotive GmbH",
  website: "https://brandt-automotive.de",
  offer_summary: "Fleet retrofit programmes for mid-size logistics operators.",
  icp: "Operators running 50–400 vans on mixed-age fleets.",
  value_proposition:
    "Cuts downtime per vehicle by scheduling retrofits around depot windows.",
  usp: "The only provider that fits around an operator's own depot calendar.",
  customer_pains: "Vehicles off the road during peak weeks.",
  desired_outcomes: "Predictable retrofit slots and a fixed per-vehicle price.",
  buying_center: "Fleet manager decides; finance signs.",
  buying_intents: "Depot expansion, emissions deadlines.",
  common_objections: "Downtime risk, and whether the quote holds.",
  sales_motion: "Field, with a depot visit before the quote.",
  fields: ["offer_summary", "icp", "usp"],
};

const CAPABILITIES = {
  read_enabled: true,
  write_enabled: true,
  rollout: "onboarding",
};

function story(
  capabilities: Record<string, unknown>,
  allow: Parameters<typeof meRoute>[0],
  company: Record<string, unknown> | null = PROFILE,
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /company/context/capabilities": () => jsonResponse(capabilities),
      "GET /company": () =>
        company ? jsonResponse(company) : jsonResponse({}, 404),
    });
    return (
      <StoryProviders>
        <CompanyContextCard />
      </StoryProviders>
    );
  };
}

const EDITOR = { organization: ["read", "update"] } as const;
const READER = { organization: ["read"] } as const;

const meta: Meta<typeof CompanyContextCard> = {
  title: "Settings/Admin settings/General/Company profile",
  component: CompanyContextCard,
};
export default meta;
type Story = StoryObj<typeof CompanyContextCard>;

export const Filled: Story = { render: story(CAPABILITIES, EDITOR) };

// A permission, not a rollout: the profile stays readable and says once that it
// is not this reader's to change.
export const ReadOnly: Story = { render: story(CAPABILITIES, READER) };

// The installation cannot read a company profile at all — a capability this
// deployment does not have, which is the one cause that justifies the surface
// being absent rather than withheld.
export const NotEnabledHere: Story = {
  render: story({ ...CAPABILITIES, read_enabled: false }, EDITOR),
};

// The filled profile in dark, and this is the surface where that is not a
// formality: the card is accent-toned, and the trust block sits on a
// `PanelPlate` — a RECESSED ground. A translucent tint composites over a
// recess differently than over the panel face, so the accent wash and the
// callouts inside it are the pair to look at, not the text.
export const FilledDark: Story = {
  globals: { theme: "dark" },
  render: story(CAPABILITIES, EDITOR),
};

// The profile at 390px. The form is a two-column grid that collapses to one at
// 760px, so this is the only story that renders the collapsed layout at all —
// and the website field carries a full URL with no space to break at.
export const FilledPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story(CAPABILITIES, EDITOR),
};
