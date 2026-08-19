// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCoreScene } from "./margince-core";
import { BEHAVIOUR } from "./margince-core-motion";

/**
 * WDS-CORE-2 says the state vocabulary is closed. A catalog showing four of the
 * eight is how a closed vocabulary quietly becomes an open one, so every state
 * gets a story — including the three nobody demos: `flagged`, `disconnected` and
 * `error`, the states a reviewer never asks to see and a user meets on a bad day.
 *
 * **The vocabulary is the agent's work lifecycle**, in order: dormant →
 * ingesting → reasoning → drafting → applied, plus the three ways a run stops. There is no `listening`: Margince's agent works overnight over captured
 * activity and stages proposals a human confirms — it never holds a conversation,
 * and a state naming one would be the product claiming something it does not do.
 *
 * **State is motion first and colour second.** Each state owns one movement
 * archetype (`margince-core-motion.ts`) and one colour triple
 * (`margince-core.css`). Two consequences worth knowing before reading the eight:
 *
 *  - A still frame is not the story. `ingesting` and `reasoning` sit in
 *    neighbouring greens and are told apart by what the mass DOES — one docks
 *    satellites and swells, the other fuses and spins — so a state story has to
 *    be watched. `margince-core-motion.test.ts` pins the formations, which is
 *    where a movement can be asserted instead of eyeballed.
 *  - The condition a user acts on is never the orb. Every surface that shows a
 *    Core also states its condition in words beside it, which is what makes the
 *    orb safe to be `aria-hidden`.
 *
 * `Ladder` at the end of this file shows all eight at once, which is the view that
 * catches two states having drifted into looking alike.
 */
const meta = {
  title: "Design System/Margince Core",
  component: MarginceCoreScene,
  parameters: { layout: "centered" },
} satisfies Meta<typeof MarginceCoreScene>;
export default meta;

type Story = StoryObj<typeof meta>;

/**
 * Rest, and where the Core spends nearly all of its life: nothing staged,
 * nothing running. One body, one slow rotation, no dot breaking rank — a
 * formation that reorganises while idle reads as an agent working, which is a
 * claim an idle CRM must not make.
 */
export const Dormant: Story = { args: { state: "dormant" } };

/**
 * Captured calls, mail and meetings arriving. Satellites dock into the mass one
 * at a time and it swells with each, so intake reads as volume taken on rather
 * than as a pulse.
 */
export const Ingesting: Story = { args: { state: "ingesting" } };

/**
 * Traversing the context graph, matching records against evidence. The four fuse
 * into one working mass and spiral — the only state that spins, and the fastest
 * thing the Core ever does.
 */
export const Reasoning: Story = { args: { state: "reasoning" } };

/**
 * Composing staged proposals, one at a time: the mass pinches off a piece, sends
 * it out on a long arc and closes again, recoiling each time one leaves. The
 * recoil is what gives the body mass — a piece leaving something that does not
 * react is just a dot moving.
 */
export const Drafting: Story = { args: { state: "drafting" } };

/**
 * A human confirmed. The dots leave the goo filter behind and draw a check mark,
 * short stroke then long — the only state that draws a symbol, and the only one
 * that should feel finished.
 */
export const Applied: Story = { args: { state: "applied" } };

/**
 * The ring is the optional half of WDS-CORE-2, and a ring rather than a bar
 * because the Core is already the thing being waited on.
 *
 * `progress` is genuinely optional rather than defaulted to 0: omit it and no
 * ring renders at all, which is why every other story here has none. A 0% ring
 * and no ring say different things — one is a job that has not moved, the other
 * is a job with no measurable length.
 */
export const IngestingWithProgress: Story = {
  args: { state: "ingesting", progress: 0.58 },
};

/**
 * A record contradicts reality, or an action needs permission it does not have.
 * Held apart under tension, shivering, never orbiting: fast and deliberately
 * going nowhere, which is what separates a problem from progress.
 */
export const Flagged: Story = { args: { state: "flagged" } };

/**
 * A source the agent cannot reach — the mailbox, the calendar, an MCP connection.
 * The mass splits and keeps reaching across; the thread stretches, thins and
 * snaps. Desaturated on purpose: nothing is wrong with the data, the agent simply
 * cannot get to it.
 */
export const Disconnected: Story = { args: { state: "disconnected" } };

/**
 * The run failed. All four collapse into one red mass with a slow heartbeat. Red
 * is the one colour outside the product's palette, which is exactly why it works
 * here — nothing else in Margince looks like this.
 */
export const Errored: Story = { args: { state: "error" } };

/**
 * Both size presets at once, because the difference is not only 230px against
 * 150px.
 *
 * The glass thins as the ball shrinks: the rim darkening and the edge are fixed
 * weights, so on a small disc they cover a far bigger share of it and turn the
 * orb grey — and the dots grow, because a dot that is proportionally right at
 * 230px is a hairline at 34px. Those rungs are container queries on the Core's
 * own box, so a layout that sizes one through `--coreGlass` gets the right
 * treatment without knowing they exist.
 *
 * Review this one at a desktop width: below 900px the stylesheet takes the hero
 * down to the md geometry, and then the two are the same ball twice.
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
      <MarginceCoreScene state="reasoning" />
      <MarginceCoreScene state="reasoning" size="md" />
    </div>
  ),
};

/**
 * Every state at once, in lifecycle order.
 *
 * This is the review a per-state story cannot give: two states drifting into the
 * same movement is invisible when they sit on separate pages and obvious here.
 * Read across the rows — each formation should be nameable without its caption.
 */
export const Ladder: Story = {
  render: () => (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(4, minmax(0, 1fr))",
        gap: "var(--space-5)",
      }}
    >
      {Object.keys(BEHAVIOUR).map((state) => (
        <figure
          key={state}
          style={{
            margin: 0,
            display: "grid",
            justifyItems: "center",
            gap: "var(--space-2)",
          }}
        >
          <MarginceCoreScene
            state={state as keyof typeof BEHAVIOUR}
            size="md"
            feed={false}
          />
          <figcaption
            style={{
              color: "var(--textMeta)",
              fontFamily: "var(--f-mono)",
              fontSize: "11px",
              letterSpacing: "0.08em",
              textTransform: "uppercase",
            }}
          >
            {state}
          </figcaption>
        </figure>
      ))}
    </div>
  ),
};

/**
 * The feed off, which is the honest setting wherever nothing is arriving: the
 * motes are context reaching the Core, so a Core that is merely present must not
 * draw them.
 *
 * It is also what a Core sitting next to copy needs. The workbench header and the
 * dock both run `feed={false}` for exactly that reason — a mote crossing a
 * paragraph is not atmosphere, it is a bug that moves. Where the layout wants
 * motes but a shorter throw, `--coreFeedReach` pulls the field in instead of
 * switching it off.
 */
export const WithoutFeed: Story = {
  args: { state: "dormant", feed: false },
};
