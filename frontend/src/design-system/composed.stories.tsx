// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { Button } from "./atoms";
import { RecordView, type TimelineEntry } from "./composed";

// RecordView's timeline gained an optional per-row `actions` slot (the Reply /
// Relink cluster the 360 screens mount). These stories exercise both shapes:
// rows without an affordance render exactly as before, and rows that carry an
// action node get the right-aligned slot — so a render regression in either
// path is caught here rather than only in the screen that composes it.

const emailEntry: TimelineEntry = {
  id: "a1",
  kind: "email",
  title: "Re: Q3 renewal terms",
  atIso: "2026-07-01T09:12:00Z",
  provenance: { kind: "human" },
};
const meetingEntry: TimelineEntry = {
  id: "a2",
  kind: "meeting",
  title: "Discovery call",
  atIso: "2026-06-24T14:00:00Z",
  provenance: { kind: "agent", agent: "capture" },
};
const noteEntry: TimelineEntry = {
  id: "a3",
  kind: "note",
  title: "Left a voicemail",
  atIso: "2026-06-20T16:30:00Z",
  provenance: { kind: "human" },
};
const baseTimeline: TimelineEntry[] = [emailEntry, meetingEntry, noteEntry];

const meta: Meta<typeof RecordView> = {
  title: "Design System/RecordView",
  component: RecordView,
};
export default meta;

type Story = StoryObj<typeof RecordView>;

// The unchanged shape: no row carries an action, so every entry renders as it
// did before the slot existed.
export const Default: Story = {
  args: {
    name: "Acme GmbH",
    subtitle: "Enterprise · Munich",
    zone: "Europe/Berlin",
    timeline: baseTimeline,
  },
};

// The new slot: the email row carries a Reply action, the meeting row a Relink
// action, and the note row none — the right-aligned cluster only appears where
// an affordance is supplied.
export const WithRowActions: Story = {
  args: {
    name: "Acme GmbH",
    subtitle: "Enterprise · Munich",
    zone: "Europe/Berlin",
    timeline: [
      {
        ...emailEntry,
        actions: (
          <Button small onClick={() => {}}>
            Reply
          </Button>
        ),
      },
      {
        ...meetingEntry,
        actions: (
          <Button small onClick={() => {}}>
            Relink
          </Button>
        ),
      },
      noteEntry,
    ],
  },
};
