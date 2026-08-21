// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { MarginceCoreScene } from "./margince-core";
import { BEHAVIOUR } from "./margince-core-motion";

/**
 * WDS-CORE-2 says the state vocabulary is closed. A catalog showing three of
 * the five is how a closed vocabulary quietly becomes an open one, so every
 * state gets a story, including the two nobody demos: `warning` and `error`,
 * the states a reviewer never asks to see and a user meets on a bad day.
 *
 * **The vocabulary is the agent's work lifecycle**, in order: idle → ingest →
 * working, and back to idle when a run settles, plus the two ways it stops.
 * There is no `listening`: Margince's agent works overnight over captured
 * activity and stages proposals a human confirms — it never holds a
 * conversation, and a state naming one would be the product claiming
 * something it does not do.
 *
 * **State is motion first and colour second.** Each state owns one distinct
 * `level` / `speed` / `pulse` / `ingest` signature (`margince-core-motion.ts`),
 * driven straight into the shader, before it owns a colour triple
 * (`margince-core.css`). Two consequences worth knowing before reading the five:
 *
 *  - A still frame is not the story. `ingest` and `working` sit in
 *    neighbouring greens and are told apart by how the ribbons move: one turns
 *    material inward, the other runs hot and fast. `margince-core-motion.test.ts`
 *    pins the signature, which is where a movement can be asserted instead of
 *    eyeballed.
 *  - The condition a user acts on is never the orb. Every surface that shows a
 *    Core also states its condition in words beside it, which is what makes the
 *    orb safe to be `aria-hidden`.
 *
 * `Ladder` at the end of this file shows all five at once, which is the view that
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
 * nothing running. Low energy, slow phase, a shallow breath — an idle state
 * with more going on than that reads as work nobody asked for. A finished run
 * settles back here rather than into a state of its own.
 */
export const Idle: Story = { args: { state: "idle" } };

/**
 * Captured calls, mail and meetings arriving. `ingest` sits at its ceiling here
 * and nowhere else, the whirlpool that reads as material moving from the glass
 * to the centre rather than the object simply being busy.
 */
export const Ingesting: Story = { args: { state: "ingest" } };

/**
 * Traversing the context graph, matching records against evidence, composing
 * staged proposals. The fastest phase of the five and the shallowest breath —
 * thought reads as speed, not as a pulse.
 */
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
export const IngestWithProgress: Story = {
  args: { state: "ingest", progress: 0.58 },
};

/**
 * Stopped and waiting for a person: a record contradicts reality, an action
 * needs permission it does not have, a source it cannot reach, a licence it
 * does not carry. Amber, slow, and breathing hard enough to be caught out of
 * the corner of an eye — the orb only ever reports that a person is needed,
 * never which of these it is; the surface around it says that in words.
 */
export const Warning: Story = { args: { state: "warning" } };

/**
 * The run failed. Red, the one colour outside the product's palette, and the
 * hardest breath of the five.
 */
export const Errored: Story = { args: { state: "error" } };

/**
 * Both size presets at once, because the difference is not only 230px against
 * 150px.
 *
 * The glass thins as the ball shrinks: the rim darkening and the edge are fixed
 * weights, so on a small disc they cover a far bigger share of it and turn the
 * orb grey. Those rungs are container queries on the Core's own box, so a layout
 * that sizes one through `--coreGlass` gets the right treatment without knowing
 * they exist.
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
      <MarginceCoreScene state="working" />
      <MarginceCoreScene state="working" size="md" />
    </div>
  ),
};

/**
 * The two ways a Core is dressed. On a dark surface it is emissive: it adds its
 * own light and the ribbons are the object. On paper it goes opaque and dark,
 * with the ribbons glowing inside it — an emissive ball on white is a smudge, so
 * `surface="dark"` exists for hosts that are dark in both themes, like the
 * workspace rail, and never follows the page's own theme the way `auto` does.
 */
export const Surfaces: Story = {
  render: () => (
    <div
      style={{
        display: "flex",
        gap: "var(--space-6)",
      }}
    >
      <div style={{ padding: "var(--space-6)", background: "var(--bgPage)" }}>
        <MarginceCoreScene state="working" size="md" surface="auto" />
      </div>
      <div style={{ padding: "var(--space-6)", background: "var(--bgRail)" }}>
        <MarginceCoreScene state="working" size="md" surface="dark" />
      </div>
    </div>
  ),
};

/**
 * Every state at once, in lifecycle order.
 *
 * This is the review a per-state story cannot give: two states drifting into the
 * same movement is invisible when they sit on separate pages and obvious here.
 * Read across the rows — each state should be nameable without its caption.
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
 * The feed off, which is the honest setting wherever nothing is arriving: `feed`
 * raises the intake floor the shader draws with, so a Core that is merely
 * present must not carry it.
 *
 * It is also what a Core sitting next to copy needs. The workbench header and
 * the dock both run `feed={false}` for exactly that reason.
 */
export const WithoutFeed: Story = {
  args: { state: "idle", feed: false },
};
