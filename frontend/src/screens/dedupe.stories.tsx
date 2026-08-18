// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { DedupeScreen } from "./dedupe";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// DedupeScreen (M4, DH-EXT-1/2): the confidence-sorted queue of open candidate
// pairs, each a Card carrying the detection-time evidence table and the two
// dispositions. It reads GET /dedupe/candidates and nothing else — no session
// probe, so the queue's own states are the whole story surface.

type Candidate = components["schemas"]["DedupeCandidate"];

function pair(overrides: Partial<Candidate> = {}): Candidate {
  return {
    id: "dc-1",
    entity_type: "person",
    left_id: "p-1",
    right_id: "p-2",
    confidence: 0.92,
    evidence: [
      {
        field: "full_name",
        left_value: "Katharina Brandt",
        right_value: "Katharina Brandt",
        signal: "agree",
        score: 1,
      },
      {
        field: "email",
        left_value: "k.brandt@nordwerk.test",
        right_value: null,
        signal: "one_sided",
      },
      {
        field: "org",
        left_value: "Nordwerk GmbH",
        right_value: "Nordwerk GmbH",
        signal: "agree",
      },
    ],
    status: "open",
    created_at: "2026-08-11T09:20:00Z",
    ...overrides,
  };
}

function queue(...candidates: Candidate[]) {
  return () => jsonResponse({ data: candidates, page: { next_cursor: null } });
}

const meta: Meta<typeof DedupeScreen> = {
  title: "Records/Dedupe",
  component: DedupeScreen,
};
export default meta;
type Story = StoryObj<typeof DedupeScreen>;

// The ordinary pair: agreeing name, one side-only address, the winner chosen
// from the table's own column headers.
export const Pair: Story = {
  render: () => {
    installFetchStub({ "GET /dedupe/candidates": queue(pair()) });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
};

// A colliding field is the reason to look before merging, and the row says so
// in the danger tone (`.dedupe-evidence tr[data-signal="collide"]`) — the one
// distinction the design system's table cannot know about. Rendered beside an
// agreeing pair so the two tones can be compared in one screenshot, in both
// themes.
export const CollidingSignal: Story = {
  render: () => {
    installFetchStub({
      "GET /dedupe/candidates": queue(
        pair({
          id: "dc-2",
          entity_type: "organization",
          confidence: 0.64,
          evidence: [
            {
              field: "org",
              left_value: "Nordwerk GmbH",
              right_value: "Nordwerk AG",
              signal: "collide",
              score: 0.71,
            },
            {
              field: "domain",
              left_value: "nordwerk.test",
              right_value: "nordwerk.test",
              signal: "agree",
            },
            {
              field: "vat_id",
              left_value: "DE111111111",
              right_value: "DE222222222",
              signal: "collide",
            },
          ],
        }),
        pair(),
      ),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
};

// The same pair in the dark theme: every derived colour is a color-mix() of a
// canonical token and follows the dark accent lift, so the danger row and the
// card's own ground have to be looked at twice.
export const CollidingSignalDark: Story = {
  globals: { theme: "dark" },
  render: CollidingSignal.render,
};

// An answered read with no rows: the ONE state allowed to say the queue is
// clear (SurfaceState `empty`).
export const Empty: Story = {
  render: () => {
    installFetchStub({ "GET /dedupe/candidates": queue() });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
};

// A read still in flight: SurfaceState's `loading` shimmer plus the spoken
// line, and never the empty sentence — a pending queue knows nothing about how
// many pairs are waiting. The promise deliberately never settles.
export const Loading: Story = {
  render: () => {
    installFetchStub({
      "GET /dedupe/candidates": () => new Promise<Response>(() => {}),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
};

// The decision notice: a pair the server answered as not-a-duplicate, with the
// way back sitting in the Callout's own action slot (`.link-button`, so the undo
// never competes with the Merge button that is still the page's primary verb).
// Driven through the control rather than a hand-set flag — the notice exists
// only after a write lands.
export const DecisionSaved: Story = {
  render: () => {
    installFetchStub({
      "GET /dedupe/candidates": queue(pair()),
      "POST /dedupe/candidates/dc-1/disposition": () =>
        jsonResponse({ ...pair(), status: "not_a_duplicate" }),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Not a duplicate" }),
    );
  },
};

// The write that was REFUSED: the queue still reads, and the failure is a danger
// Callout above it rather than a bare red line.
export const DecisionRefused: Story = {
  render: () => {
    installFetchStub({
      "GET /dedupe/candidates": queue(pair()),
      "POST /dedupe/candidates/dc-1/disposition": () =>
        jsonResponse(
          {
            title: "Conflict",
            status: 409,
            detail: "This pair was already decided by someone else.",
          },
          409,
        ),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Not a duplicate" }),
    );
  },
};

// A failed read reports the server's own sentence in a danger Callout, rather
// than an empty queue or a generic "could not be loaded".
export const ReadFailed: Story = {
  render: () => {
    installFetchStub({
      "GET /dedupe/candidates": () =>
        jsonResponse(
          {
            title: "Server error",
            status: 500,
            detail: "The dedupe queue could not be read. Try again shortly.",
          },
          500,
        ),
    });
    return (
      <StoryProviders>
        <DedupeScreen />
      </StoryProviders>
    );
  },
};
