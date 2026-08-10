// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCoreScene } from "./margince-core";

/**
 * WDS-CORE-2 says the state vocabulary is closed. A catalog showing four of the
 * eight is how a closed vocabulary quietly becomes an open one, so every state
 * gets a story — including the two nobody demos: `quiet` and `unavailable`, the
 * states a reviewer never asks to see and a user meets on a bad day.
 *
 * **No state changes the hue.** The Core is always the brand green, and state
 * lives in the breathing rhythm alone (`--coreBeat`): a sphere that turned amber
 * or red read as the brand changing character, and a grey one read as switched
 * off. Two consequences worth knowing before reading the eight state stories:
 *
 *  - In a still frame they look identical, and that is the design rather than a
 *    broken story — the difference is cadence, so a state story has to be
 *    WATCHED. The state-to-rhythm mapping is pinned in `margince-core.test.ts`,
 *    where a rhythm can actually be asserted instead of eyeballed.
 *  - The condition a user acts on is never the sphere. Every surface that shows
 *    a Core also states its condition in words beside it and marks it on a small
 *    status dot, which IS allowed to carry the danger and success hues.
 *
 * What the catalog CANNOT show: Storybook runs in a browser with WebGL, so these
 * are the GPU rendering. The required non-GPU rendering (WDS-CORE-3) is what the
 * vitest suite exercises, because jsdom has no context — the two halves of the
 * ladder are covered in different places on purpose.
 */
const meta = {
  title: "Design System/Margince Core",
  component: MarginceCoreScene,
  parameters: { layout: "centered" },
} satisfies Meta<typeof MarginceCoreScene>;
export default meta;

type Story = StoryObj<typeof meta>;

export const Idle: Story = { args: { state: "idle" } };

/** Waiting on the user: quicker than rest, and nothing claimed yet. */
export const Listening: Story = { args: { state: "listening" } };

/** The fastest breath in the set, and the only claim the sphere makes. */
export const Working: Story = { args: { state: "working" } };

/**
 * The ring is the optional half of WDS-CORE-2, and a ring rather than a bar
 * because the Core is already the thing being waited on.
 *
 * `progress` is genuinely optional rather than defaulted to 0: omit it and no
 * ring renders at all, which is why every other story here has none. A 0% ring
 * and no ring say different things — one is a job that has not moved, the other
 * is a job with no measurable length.
 */
export const WorkingWithProgress: Story = {
  args: { state: "working", progress: 0.58 },
};

/** Settled: the slowest breath but one. Rest, not celebration. */
export const Success: Story = { args: { state: "success" } };

/**
 * Needs a human — and the sphere does not say so in amber. `attention` is the
 * same green at an unsettled pace; what NAMES the condition is the surface's own
 * notice and its status dot.
 */
export const Attention: Story = { args: { state: "attention" } };

export const Errored: Story = { args: { state: "error" } };

/**
 * Present, not working: the slowest breath in the vocabulary, nearly still, and
 * fully saturated — desaturating it would read as a disabled control, which is
 * the one thing this state is not.
 */
export const Quiet: Story = { args: { state: "quiet" } };

/**
 * The one state that does not breathe: a server we cannot reach must not look
 * busy, and a frozen frame says that better than any colour.
 *
 * The feed stops with it, which is the same argument twice — motes arriving at a
 * Core that cannot be reached would be drawing a connection that is not there.
 */
export const Unavailable: Story = { args: { state: "unavailable" } };

/**
 * Both size presets at once, because the difference is not only 230px against
 * 150px.
 *
 * `coreBufferSize` derives the shader's internal buffer from the DISPLAYED width
 * and clamps it to 96..160, so the hero renders its liquid into 155px and the md
 * into 113px: a caller sizing a Core down buys fewer fragments rather than
 * upscaling the same ones, which is what lets one shader serve both without
 * either paying for the other. Watch the filaments — the threads hold together
 * at both, which is what the 96 floor exists for.
 *
 * Review this one at a desktop width: below 900px the stylesheet takes the hero
 * down to the md geometry, and then the two are the same sphere twice.
 */
export const Sizes: Story = {
  render: () => (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: "var(--space-6)",
      }}
    >
      <MarginceCoreScene state="working" />
      <MarginceCoreScene state="working" size="md" />
    </div>
  ),
};

/**
 * The feed off, which is the honest setting wherever nothing is arriving: the
 * motes are context reaching the Core, so a Core that is merely present must not
 * draw them.
 *
 * It is also what a Core sitting next to copy needs. The workbench header runs
 * `feed={false}` for exactly that reason — a mote crossing a paragraph is not
 * atmosphere, it is a bug that moves. Where the layout wants motes but a shorter
 * throw, `--coreFeedReach` pulls the field in instead of switching it off.
 */
export const WithoutFeed: Story = {
  args: { state: "idle", feed: false },
};
