// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CircleAlert, Info, TriangleAlert } from "lucide-react";
import { LocaleProvider } from "../i18n";
import { Button } from "./atoms";
import { Callout } from "./callout";
import { FactList } from "./factlist";

const meta: Meta = {
  title: "Design System/Callout",
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

/** The four tones together, which is the only way to judge that they differ. */
export const Tones: Story = {
  render: () => (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--space-3)",
      }}
    >
      <Callout icon={Info}>
        Capture is reading this mailbox every five minutes.
      </Callout>
      <Callout tone="warn" icon={TriangleAlert} title="Reindex needed">
        Search is answering from an index that is behind the records.
      </Callout>
      <Callout tone="danger" icon={CircleAlert} title="That did not save">
        The role changed while you were editing. Re-read it and try again.
      </Callout>
      <Callout tone="success">HubSpot is connected.</Callout>
    </div>
  ),
};

/** With an action, which is what most banners actually are. */
export const WithActions: Story = {
  render: () => (
    <Callout
      tone="warn"
      icon={TriangleAlert}
      title="Connection interrupted"
      actions={
        <>
          <Button variant="primary" small>
            Resume
          </Button>
          <Button small>Dismiss</Button>
        </>
      }
    >
      Finish connecting Claude to pick up where you left off.
    </Callout>
  ),
};

/** Prose over one line, so the paragraph rhythm inside the body is visible. */
export const Prose: Story = {
  render: () => (
    <Callout tone="danger" icon={CircleAlert} title="This cannot be undone">
      <p>Erasure removes the person and everything captured about them.</p>
      <p>A tombstone stays in the audit log. Nothing else survives.</p>
    </Callout>
  ),
};

export const Facts: Story = {
  name: "FactList",
  render: () => (
    <FactList
      numeric
      facts={[
        { key: "in", term: "Last inbound", value: "3 Feb 2026" },
        { key: "out", term: "Last outbound", value: "Never" },
        {
          key: "spend",
          term: "Spend this month",
          value: "€1,204.00",
          note: "Partial — 12 of 28 days counted",
        },
      ]}
    />
  ),
};
