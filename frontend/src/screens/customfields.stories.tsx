// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { FieldBuilder, FieldTable } from "./customfields";
import { StoryProviders } from "./story-utils";

// The custom-fields admin sub-components rendered with direct props so the
// fe-uat render lane exercises them without a network round-trip. FieldBuilder
// owns its own type/label state internally, so the currency / picklist /
// refusal variants below render the default builder and are driven into that
// state interactively (the repo has no @storybook/test `play` harness to script
// it). FieldTable is fully prop-driven, so its states are pinned by fixtures.
const meta: Meta = {
  title: "Screens/CustomFields",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

type CustomField = components["schemas"]["CustomField"];

const field = (over: Partial<CustomField> = {}): CustomField => ({
  id: "01J2Z3K4M5N6P7Q8R9S0T1U2V3",
  workspace_id: "w-1",
  object: "deal",
  label: "Renewal date",
  slug: "renewal_date",
  type: "date",
  status: "active",
  column_name: "cf_renewal_date",
  created_by: "u1",
  created_at: "2026-06-22T14:09:00Z",
  updated_at: "2026-06-22T14:09:00Z",
  version: 1,
  ...over,
});

const dealFields: CustomField[] = [
  field(),
  field({
    id: "01J2Z3K4M5N6P7Q8R9S0T1U2V4",
    label: "Deal stage reason",
    slug: "deal_stage_reason",
    type: "picklist",
    column_name: "cf_deal_stage_reason",
    options: ["Budget", "Timing", "Champion left"],
    created_by: "admin-user",
    version: 2,
  }),
  field({
    id: "01J2Z3K4M5N6P7Q8R9S0T1U2V5",
    label: "Ceiling",
    slug: "ceiling",
    type: "currency",
    column_name: "cf_ceiling",
    currency: "EUR",
    created_by: "u1",
    version: 1,
  }),
];

const noop = () => {};

export const BuilderText: Story = {
  render: () => (
    <StoryProviders>
      <FieldBuilder
        object="organization"
        pending={false}
        onSubmit={noop}
        onToast={noop}
      />
    </StoryProviders>
  ),
};

// Renders the default builder; select the Currency type to reveal the ISO-4217
// currency-code input (the builder owns its own type state — no `play` harness).
export const BuilderCurrency: Story = {
  render: () => (
    <StoryProviders>
      <FieldBuilder
        object="deal"
        pending={false}
        onSubmit={noop}
        onToast={noop}
      />
    </StoryProviders>
  ),
};

// Renders the default builder; select the Picklist type to reveal the options
// editor (reached interactively — the repo has no `play` scripting).
export const BuilderPicklist: Story = {
  render: () => (
    <StoryProviders>
      <FieldBuilder
        object="deal"
        pending={false}
        onSubmit={noop}
        onToast={noop}
      />
    </StoryProviders>
  ),
};

// Renders the default builder; type a structural label (e.g. "Link to parent
// account") to surface the refusal banner and disable Confirm.
export const BuilderRefusal: Story = {
  render: () => (
    <StoryProviders>
      <FieldBuilder
        object="organization"
        pending={false}
        onSubmit={noop}
        onToast={noop}
      />
    </StoryProviders>
  ),
};

export const TableWithFields: Story = {
  render: () => (
    <StoryProviders>
      <FieldTable
        object="deal"
        fields={dealFields}
        canManage
        meUserId="u1"
        onRename={noop}
        onArchive={noop}
      />
    </StoryProviders>
  ),
};

export const EmptyObject: Story = {
  render: () => (
    <StoryProviders>
      <FieldTable
        object="person"
        fields={[]}
        canManage
        meUserId="u1"
        onRename={noop}
        onArchive={noop}
      />
    </StoryProviders>
  ),
};

export const Retired: Story = {
  render: () => (
    <StoryProviders>
      <FieldTable
        object="deal"
        fields={[
          field({
            label: "Legacy priority",
            slug: "legacy_priority",
            column_name: "cf_legacy_priority",
            status: "retired",
          }),
        ]}
        canManage
        meUserId="u1"
        onRename={noop}
        onArchive={noop}
      />
    </StoryProviders>
  ),
};

export const NoPermission: Story = {
  render: () => (
    <StoryProviders>
      <FieldTable
        object="deal"
        fields={[field()]}
        canManage={false}
        meUserId="u1"
        onRename={noop}
        onArchive={noop}
      />
    </StoryProviders>
  ),
};
