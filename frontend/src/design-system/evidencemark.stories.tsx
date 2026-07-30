// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { EvidenceMark } from "./evidencemark";

// The one provenance affordance, in the three shapes a record page shows it:
// a value read from a page, a value a connector supplied, and a value a
// person typed — which carries no mark at all.
const meta: Meta<typeof EvidenceMark> = {
  title: "Design System/EvidenceMark",
  component: EvidenceMark,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <div style={{ maxWidth: 420, display: "grid", gap: "var(--space-4)" }}>
          <Story />
        </div>
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj<typeof EvidenceMark>;

export const ReadFromTheWeb: Story = {
  args: {
    value: "Fleet retrofits without downtime",
    source: {
      provenance: { kind: "agent", agent: "capture" },
      confidence: "high",
      snippet: "We retrofit fleets without downtime",
      sourceUrl: "https://brandt.example",
      at: "1 Jul 2026",
    },
  },
};

export const FromAConnector: Story = {
  args: {
    value: "dana@brandt.example",
    source: {
      provenance: { kind: "connector", connector: "gmail" },
      confidence: "med",
      at: "10 Jul 2026",
    },
  },
};

// A value a person typed is not marked: an underline on everything would
// teach the reader that the underline means nothing.
export const TypedByAPerson: Story = {
  args: { value: "Brandt Automotive GmbH", source: undefined },
};
