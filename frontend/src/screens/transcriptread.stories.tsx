// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import { TranscriptReadCard } from "./transcriptread";

// Reading a meeting transcript for the next steps it states (S-E04.3). The
// three terminal states are separate stories on purpose: they are the whole
// point of the surface, and "it stated nothing" drawn like "it broke" is the
// failure this card exists to prevent.

const LATEST = "GET /activities/a-1/transcript-proposals/latest";
const READ = "GET /activities/a-1/transcript-proposals/rd-1";

const base = {
  read_id: "rd-1",
  activity_id: "a-1",
  status: "done",
  line_count: 48,
  proposal_ids: [],
  created_at: "2026-08-01T09:00:00Z",
};

function stubbed(report: Record<string, unknown>) {
  installFetchStub({
    [LATEST]: () => jsonResponse(report),
    [READ]: () => jsonResponse(report),
  });
}

const meta: Meta<typeof TranscriptReadCard> = {
  title: "Screens/transcript-read",
  component: TranscriptReadCard,
};
export default meta;
type Story = StoryObj<typeof TranscriptReadCard>;

// A transcript nobody has read yet: 404 on the latest read is the honest
// "never tried", and the card offers a first reading and states nothing else.
export const NeverRead: Story = {
  render: () => {
    installFetchStub({
      [LATEST]: () => jsonResponse({ title: "Not Found" }, 404),
    });
    return (
      <StoryProviders>
        <TranscriptReadCard activityId="a-1" />
      </StoryProviders>
    );
  },
};

export const StillReading: Story = {
  render: () => {
    stubbed({ ...base, status: "running", line_count: 0 });
    return (
      <StoryProviders>
        <TranscriptReadCard activityId="a-1" />
      </StoryProviders>
    );
  },
};

export const StagedNextSteps: Story = {
  render: () => {
    stubbed({ ...base, proposal_ids: ["ap-1", "ap-2", "ap-3"] });
    return (
      <StoryProviders>
        <TranscriptReadCard activityId="a-1" />
      </StoryProviders>
    );
  },
};

// A correct empty answer. It reads as a finished reading that found nothing,
// never as a reading that fell over.
export const StatedNothing: Story = {
  render: () => {
    stubbed({
      ...base,
      status_detail: "Nobody committed to anything in this call.",
    });
    return (
      <StoryProviders>
        <TranscriptReadCard activityId="a-1" />
      </StoryProviders>
    );
  },
};

export const CouldNotRead: Story = {
  render: () => {
    stubbed({
      ...base,
      status: "failed",
      line_count: 0,
      status_detail: "The model refused: this transcript is too long.",
    });
    return (
      <StoryProviders>
        <TranscriptReadCard activityId="a-1" />
      </StoryProviders>
    );
  },
};
