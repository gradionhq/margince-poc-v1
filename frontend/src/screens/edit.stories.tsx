// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { EditRecordModal } from "./edit";

// EditRecordModal is prop-driven (no react-query/fetch inside it — the
// screen supplies `record`/`update`), so these stories render it directly
// with a prefilled record and, separately, the version-skew error copy
// EditAction shows on a 409 code:version_skew.
const meta: Meta = {
  title: "Screens/Edit",
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

const record = {
  id: "p-1",
  version: 3,
  full_name: "Anna Weber",
  title: "Head of Procurement",
};

const fields = [
  { key: "full_name", label: "create.fullName" as const, required: true },
  { key: "title", label: "create.personTitle" as const },
];

export const Prefilled: Story = {
  render: () => (
    <EditRecordModal
      open
      onClose={() => undefined}
      title="Edit contact"
      fields={fields}
      record={record}
      pending={false}
      error={null}
      onSubmit={() => undefined}
    />
  ),
};

// The friendly copy edit.tsx substitutes for the raw 409 detail on a
// version_skew conflict — never the server's literal
// "if-match version N does not match current version M".
export const VersionSkewError: Story = {
  render: () => (
    <EditRecordModal
      open
      onClose={() => undefined}
      title="Edit contact"
      fields={fields}
      record={record}
      pending={false}
      error="This record changed since you opened it — reload and try again."
      onSubmit={() => undefined}
    />
  ),
};
