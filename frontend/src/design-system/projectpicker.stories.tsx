// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { LocaleProvider } from "../i18n";
import { ProjectPicker, ScopeLine } from "./projectpicker";

// The one control every AI surface is told its project through, and the one
// line that says which project the output was narrowed to.
const meta: Meta<typeof ProjectPicker> = {
  title: "Design System/ProjectPicker",
  component: ProjectPicker,
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

type Story = StoryObj<typeof ProjectPicker>;

const projects = [
  {
    project_id: "p-erp",
    name: "ERP rollout",
    key: "ERP-27",
    phase: "delivering",
  },
  {
    project_id: "p-dc",
    name: "Datacentre migration",
    key: "DC-4",
    phase: "pursuing",
  },
] as const;

function Demo() {
  const [projectId, setProjectId] = useState("p-erp");
  return (
    <ProjectPicker
      projects={projects}
      projectId={projectId}
      onChange={setProjectId}
      scope={{
        project_id: "p-erp",
        name: "ERP rollout",
        key: "ERP-27",
        in_scope: 4,
        total: 11,
      }}
    />
  );
}

// Picking the project the server's report names prints the counted line;
// picking the other one falls back to the uncounted caption until a new
// report arrives.
export const WithCountedScope: Story = { render: () => <Demo /> };

// A surface that scopes itself — the meeting brief under a filed meeting —
// prints the line without a picker.
export const LineAlone: Story = {
  render: () => (
    <ScopeLine
      scope={{ project_id: "p-erp", name: "ERP rollout", key: "ERP-27" }}
    />
  ),
};
