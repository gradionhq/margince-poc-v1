// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ScheduledSendsScreen } from "./scheduledsends";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The queue behind "send later": what a rep has written that has not gone out
// yet, and the two things they can do about it.
//
// It is one person's own list — an unsent body and its blind-copy list are not
// workspace-readable the way a sent activity is — so there is no owner column
// here and no sharing affordance, and none of these stories is about a role.
//
// The three groups are the reading, not a filter: `held` first because it is the
// only one waiting on a person, `scheduled` as the queue proper, and everything
// settled below them. A rep with nothing scheduled reads ONE sentence rather
// than three empty blocks, which is `NothingScheduled`.
const meta: Meta = {
  title: "Work/Scheduled messages",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

// Every moment is stated in the reader's own zone unless the send names another,
// so the fixtures below carry the browser's — except `crossZone`, which is the
// case the row has to explain rather than silently re-render.
const readerZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

const waiting = {
  id: "019f7e65-fbf7-7114-b114-40af4af63ae8",
  status: "scheduled",
  scheduled_at: "2026-09-01T07:00:00Z",
  scheduled_tz: readerZone,
  subject: "The renewal quote",
  to: ["ceo@acme.example"],
  version: 3,
  created_at: "2026-08-20T09:00:00Z",
  updated_at: "2026-08-20T09:00:00Z",
};

// Stopped at fire, and the row says why in words: `consent_withdrawn` is a wire
// token, and a rep cannot act on a wire token.
const held = {
  ...waiting,
  id: "019f7e65-fbf7-7114-b114-40af4af63ae9",
  status: "held",
  held_reason: "consent_withdrawn",
  subject: "The follow-up",
  to: ["ops@acme.example", "cfo@acme.example", "legal@acme.example"],
};

// Picked in another zone, which the row NAMES. A rep who scheduled 09:00 from
// Berlin and reads from Hanoi must not be shown 14:00 with no explanation, and
// must not be shown 09:00 with no zone either.
const crossZone = {
  ...waiting,
  id: "019f7e65-fbf7-7114-b114-40af4af63aeb",
  scheduled_tz: readerZone === "Asia/Tokyo" ? "Europe/Berlin" : "Asia/Tokyo",
  subject: "The board update",
};

const settled = [
  {
    ...waiting,
    id: "019f7e65-fbf7-7114-b114-40af4af63aec",
    status: "sent",
    subject: "Last week's summary",
  },
  {
    ...waiting,
    id: "019f7e65-fbf7-7114-b114-40af4af63aed",
    status: "cancelled",
    subject: "The price change note",
  },
];

function stubQueue(rows: unknown[]): void {
  installFetchStub({ "GET /scheduled-sends": () => jsonResponse(rows) });
}

function Screen() {
  return (
    <StoryProviders>
      <ScheduledSendsScreen />
    </StoryProviders>
  );
}

export const FullQueue: Story = {
  render: () => {
    stubQueue([held, waiting, crossZone, ...settled]);
    return <Screen />;
  },
};

// The two verbs a waiting message offers. Only the queue proper carries them: a
// message that has gone offers neither, because the server would refuse both.
export const OnlyWaiting: Story = {
  render: () => {
    stubQueue([waiting, crossZone]);
    return <Screen />;
  },
};

// Moving is inline and unconfirmed; the picker opens seeded from the send's own
// moment, so a rep who opens it and saves without touching it does not move the
// message.
export const MovingAMessage: Story = {
  render: () => {
    stubQueue([waiting]);
    return <Screen />;
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("button", {
        name: "Change moment",
      }),
    );
  },
};

// Withdrawing IS confirmed, and the asymmetry is the point: a moved message can
// be moved back, while a withdrawn one has to be written again from nothing —
// the approval it carried does not survive it.
export const WithdrawConfirm: Story = {
  render: () => {
    stubQueue([waiting]);
    return <Screen />;
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(
      await within(canvasElement).findByRole("button", { name: "Withdraw" }),
    );
  },
};

// One sentence, not three. A rep who has never scheduled anything is not reading
// three findings, and three empty blocks make a page that is simply clear look
// broken.
export const NothingScheduled: Story = {
  render: () => {
    stubQueue([]);
    return <Screen />;
  },
};

// The German copy, whose length is the thing worth looking at on a row that
// already carries a subject, a recipient line, a moment, a badge and two verbs.
export const FullQueueGerman: Story = {
  render: () => {
    stubQueue([held, waiting, crossZone]);
    return (
      <StoryProviders locale="de">
        <ScheduledSendsScreen />
      </StoryProviders>
    );
  },
};
