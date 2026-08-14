// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { FieldBuilder, FieldTable } from "./customfields";
import { StoryProviders } from "./story-utils";

// The custom-fields admin sub-components rendered with direct props so the
// fe-uat render lane exercises them without a network round-trip. FieldBuilder
// owns its own type/label state internally, so the currency / picklist /
// refusal variants drive it there with `play` before the frame is taken — a
// story that only mounted the default builder three times captured the same
// screenshot three times and proved nothing about any of the three branches.
// FieldTable is fully prop-driven, so its states are pinned by fixtures.
const meta: Meta = {
  title: "Settings/Organization/Data model/Custom fields",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

type CustomField = components["schemas"]["CustomField"];

const field = (over: Partial<CustomField> = {}): CustomField => ({
  id: "01J2Z3K4M5N6P7Q8R9S0T1U2V3",
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

// The Currency type, chosen: the ISO-4217 currency-code input appears under
// the type control and the DDL preview below picks up the numeric column.
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
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Label"), "Ceiling");
    await userEvent.click(canvas.getByRole("button", { name: "Currency" }));
    await canvas.findByLabelText("Currency code");
  },
};

// The Picklist type, chosen, with two options typed in: the options editor is
// the only type whose shape the builder validates before Confirm goes live.
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
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Label"), "Stage reason");
    await userEvent.click(canvas.getByRole("button", { name: "Picklist" }));
    const [first] = await canvas.findAllByLabelText("Option label");
    await userEvent.type(first, "Budget");
    await userEvent.click(canvas.getByRole("button", { name: "Add option" }));
    const rows = await canvas.findAllByLabelText("Option label");
    await userEvent.type(rows[rows.length - 1], "Timing");
  },
};

// A structural label refused up front: the banner explains why a link between
// objects is not a field, and Confirm stays dead while the label says it.
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
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      canvas.getByLabelText("Label"),
      "Link to parent account",
    );
    await canvas.findByRole("alert");
  },
};

export const TableWithFields: Story = {
  render: () => (
    <StoryProviders>
      <FieldTable
        object="deal"
        fields={dealFields}
        canEdit
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
        canEdit
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
        canEdit
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
        canEdit={false}
        meUserId="u1"
        onRename={noop}
        onArchive={noop}
      />
    </StoryProviders>
  ),
};
