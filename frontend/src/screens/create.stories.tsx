// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { LocaleProvider } from "../i18n";
import {
  type CreateField,
  CreateRecordModal,
  type FormRows,
  RecordFormBody,
} from "./create";

// The shared create-record form (RecordFormBody + CreateRecordModal): both
// take their fields/values/error state as plain props, so no react-query or
// fetch mocking is needed here — the fe-uat render gate (fe-uat.mjs) drives
// these states directly.
const meta: Meta = {
  title: "Screens/Create",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

// A mix of text + select + a repeatable "emails" field — the contact create
// form's actual shape (people.tsx's contactCreateFields, trimmed to the
// fields that show every control kind in one screen).
const contactFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "title", label: "create.personTitle" },
  {
    key: "size_band",
    label: "create.sizeBand",
    type: "select",
    options: [
      { value: "1-10", label: "1-10" },
      { value: "11-50", label: "11-50" },
    ],
  },
  {
    key: "emails",
    label: "create.email",
    type: "repeatable",
    addLabel: "field.addEmail",
    rowFields: [
      { key: "email", label: "create.email", type: "email", required: true },
      {
        key: "email_type",
        label: "field.emailType",
        type: "select",
        options: [
          { value: "work", label: "field.emailWork" },
          { value: "personal", label: "field.emailPersonal" },
        ],
      },
    ],
    primaryKey: "is_primary",
  },
];

function OpenModal({
  error,
  existing,
}: Readonly<{
  error?: string | null;
  existing?: { id: string; code: string } | null;
}>) {
  return (
    <CreateRecordModal
      open
      onClose={() => undefined}
      title="New contact"
      fields={contactFields}
      pending={false}
      error={error ?? null}
      existing={existing ?? null}
      resolveExisting={(_code, id) => ({ screen: "contacts", id })}
      onSubmit={() => undefined}
    />
  );
}

export const Open: Story = {
  render: () => <OpenModal />,
};

// A duplicate (409) dedupe error: the server's detail renders verbatim plus
// a "view existing" link to the collided record.
export const DedupeError: Story = {
  render: () => (
    <OpenModal
      error="email already in use"
      existing={{ id: "01X", code: "duplicate_email" }}
    />
  ),
};

// The bare form body (no modal chrome) with its fields already filled in,
// for a tighter render of just the field/control layer.
export const FormBodyFilled: Story = {
  render: () => {
    function Filled() {
      const [values, setValues] = useState<Record<string, string>>({
        full_name: "Peter Neu",
        title: "Head of Procurement",
      });
      const [rows, setRows] = useState<FormRows>({
        emails: [
          {
            email: "peter@neu.example",
            email_type: "work",
            is_primary: "true",
          },
        ],
      });
      return (
        <RecordFormBody
          fields={contactFields}
          values={values}
          setValues={setValues}
          rows={rows}
          setRows={setRows}
          pending={false}
          error={null}
          onSubmit={() => undefined}
          onClose={() => undefined}
          submitLabelKey="create.save"
        />
      );
    }
    return <Filled />;
  },
};
