// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CustomFieldsCard } from "./customfields.card";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The custom fields an installation added, shown on the record they were added
// to. Each row is one field's own type rendered the way that type reads — a
// currency with its code, a picklist as its chosen value, a boolean as yes/no
// rather than true/false.
const base = {
  object: "organization" as const,
  status: "active" as const,
  created_by: "u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const FIELDS = [
  {
    ...base,
    id: "cf-1",
    label: "Fleet size",
    slug: "fleet_size",
    column_name: "cf_fleet_size",
    type: "number" as const,
  },
  {
    ...base,
    id: "cf-2",
    label: "Annual retrofit budget",
    slug: "retrofit_budget",
    column_name: "cf_retrofit_budget",
    type: "currency" as const,
    currency: "EUR",
  },
  {
    ...base,
    id: "cf-3",
    label: "Depot region",
    slug: "depot_region",
    column_name: "cf_depot_region",
    type: "picklist" as const,
    options: ["North", "South", "West"],
  },
  {
    ...base,
    id: "cf-4",
    label: "Framework agreement",
    slug: "framework",
    column_name: "cf_framework",
    type: "boolean" as const,
  },
];

const RECORD = {
  cf_fleet_size: 220,
  cf_retrofit_budget: "48000.00",
  cf_depot_region: "North",
  cf_framework: true,
};

function story(
  fields: Record<string, unknown>[],
  record: Record<string, unknown>,
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ custom_field: ["read"] }),
      "GET /custom-fields": () => jsonResponse({ data: fields }),
    });
    return (
      <StoryProviders>
        <CustomFieldsCard object="organization" record={record} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof CustomFieldsCard> = {
  title: "Records/Company/Custom fields",
  component: CustomFieldsCard,
};
export default meta;
type Story = StoryObj<typeof CustomFieldsCard>;

export const EveryType: Story = { render: story(FIELDS, RECORD) };

// A field the installation defined and this record has no value for. The row is
// still drawn: an absent row would say the field does not exist.
export const SomeUnset: Story = {
  render: story(FIELDS, { cf_fleet_size: 220 }),
};

// No custom fields defined at all — the card has nothing to say and says
// nothing, which is the one honest reason for a surface to be absent.
export const NoneDefined: Story = { render: story([], {}) };
