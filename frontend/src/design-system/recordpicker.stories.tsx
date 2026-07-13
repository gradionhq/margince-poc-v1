// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { RecordPicker, type RecordPickerCandidate } from "./recordpicker";

// Stories are the render surface the change-scoped fe-uat capture gate drives
// (frontend/scripts/fe-uat.mjs) — see atoms.stories.tsx for the convention.
// This is RecordPicker's first caller (Task 2.1): a fixed in-memory
// candidate list stands in for a real search transport until the offer
// header (Task 2.3) and line-item pickers (Task 3.3) wire a live one.
const meta: Meta = {
  title: "Design System/RecordPicker",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const fixtureCandidates: RecordPickerCandidate[] = [
  { id: "org-1", name: "Brandt Automotive" },
  { id: "org-2", name: "Weber Logistics" },
  { id: "org-3", name: "Fischer & Wagner" },
];

function searchFixture(q: string): Promise<RecordPickerCandidate[]> {
  const needle = q.toLowerCase();
  return Promise.resolve(
    fixtureCandidates.filter((candidate) =>
      candidate.name.toLowerCase().includes(needle),
    ),
  );
}

function PickerDemo() {
  const [selected, setSelected] = useState<RecordPickerCandidate | null>(null);
  return (
    <div style={{ maxWidth: 320 }}>
      <RecordPicker
        label="Search organizations…"
        searchTargets={searchFixture}
        onPick={setSelected}
        selected={selected}
      />
      {selected && <p style={{ marginTop: 8 }}>Picked: {selected.name}</p>}
    </div>
  );
}

export const Default: Story = {
  render: () => <PickerDemo />,
};
