// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
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
  // The provenance sidecar, in the shape the server actually sends: one row per
  // confirmed statement. The story used to pass three bare field NAMES here,
  // which counted as three confirmed statements in the footer and produced no
  // provenance at all — so the one state this card draws differently from a
  // plain value, a site-read value under an evidence mark, was invisible in
  // every story of it.
  fields: [
    {
      field: "display_name",
      value: "Brandt Automotive GmbH",
      source: "human",
      captured_by: "human:u-7",
      updated_at: "2026-05-02T08:00:00Z",
    },
    {
      field: "offer_summary",
      value: "Fleet retrofit programmes for mid-size logistics operators.",
      source: "site_read",
      captured_by: "agent:site-read",
      evidence_snippet:
        "We run retrofit programmes for mid-size logistics operators.",
      source_url: "https://brandt-automotive.de/leistungen",
      confidence: 0.86,
      updated_at: "2026-08-01T09:00:00Z",
    },
    {
      field: "usp",
      value:
        "The only provider that fits around an operator's own depot calendar.",
      source: "site_read",
      captured_by: "agent:site-read",
      evidence_snippet: "We plan around your depot calendar, not ours.",
      source_url: "https://brandt-automotive.de/warum-wir",
      confidence: 0.62,
      updated_at: "2026-08-01T09:00:00Z",
    },
  ],
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
// formality: the lead card is accent-toned, so its header band and its footer
// rule are both tints that composite over the panel face. The accent wash, the
// count in the footer and the dotted evidence marks in the value column are the
// set to look at, not the text.
export const FilledDark: Story = {
  globals: { theme: "dark" },
  render: story(CAPABILITIES, EDITOR),
};

// The profile at 390px, which is where the row language earns or loses its
// keep: below 640px a row stops holding its answer to the right and stacks the
// naming above it, and the website value is a full URL with no space to break
// at.
export const FilledPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story(CAPABILITIES, EDITOR),
};

// The editor the row verbs open: seventeen fields in their three sections, one
// Save, and focus on the fact whose verb was pressed. The rows behind it are
// answers; this is the only place on the page that is a form.
export const EditingEssentials: Story = {
  render: story(CAPABILITIES, EDITOR),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Edit What do you sell?" }),
    );
  },
};

// A profile nobody has filled in past the three the save demands. Every
// elaboration reads "Not set" behind its disclosure, and the footer counts no
// confirmed statements — the state a fresh installation opens on, and the one
// where an empty right column would have been indistinguishable from a row that
// failed to render.
export const Sparse: Story = {
  render: story(CAPABILITIES, EDITOR, {
    display_name: "Brandt Automotive GmbH",
    offer_summary:
      "Fleet retrofit programmes for mid-size logistics operators.",
    icp: "Operators running 50–400 vans on mixed-age fleets.",
    fields: [],
  }),
};
