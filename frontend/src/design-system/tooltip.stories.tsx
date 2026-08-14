// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ReactNode } from "react";
import { useTruncationTooltip } from "./tooltip";

/**
 * The tip that reveals a string its own row had to truncate.
 *
 * What these stories are for, in order of what they actually catch: a tip only
 * appears where the text really is clipped (Clipped And Whole shows both in one
 * column, and only the first answers), it escapes the `overflow: hidden` that
 * did the clipping rather than being cut off by it (In A Narrow Card), and it
 * flips below its anchor when there is no room above (Near The Top). Hover a row
 * or tab to it — a clipped row takes a tab stop and an unclipped one does not.
 * Flip the Theme toolbar to see the dark rendering; every value is a token.
 */
const meta = {
  title: "Design System/Tooltip",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

type Story = StoryObj<typeof meta>;

const LONG =
  "Asigbsdakjgbndkjgbndfkjgndfkjngkjdfsngjdngjndfgbnsdfkjgbdkjfbgkjgjhasfhisj Holdings";
const SHORT = "Sontana";

// The shape all three call sites share: one line, clipped at the edge, with the
// whole string on the tip.
function Row({ text }: Readonly<{ text: string }>) {
  const tip = useTruncationTooltip<HTMLSpanElement>(text);
  return (
    <span
      ref={tip.ref}
      style={{
        display: "block",
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap",
      }}
      {...tip.trigger}
    >
      {text}
      {tip.tip}
    </span>
  );
}

function Card({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div
      style={{
        background: "var(--bgCard)",
        border: "1px solid var(--borderSubtle)",
        borderRadius: "var(--r-md)",
        display: "grid",
        gap: "var(--space-3)",
        maxWidth: 260,
        padding: "var(--space-3)",
      }}
    >
      {children}
    </div>
  );
}

/** The whole point in one frame: the long row answers, the short one does not. */
export const ClippedAndWhole: Story = {
  render: () => (
    <Card>
      <Row text={LONG} />
      <Row text={SHORT} />
    </Card>
  ),
};

/**
 * The case the component exists for. The card clips its own content, which is
 * what truncates the text — so a tip drawn inside it would be clipped by the
 * same rule. It is portalled to the body instead.
 */
export const InANarrowCard: Story = {
  render: () => (
    <div style={{ display: "grid", gap: "var(--space-3)", maxWidth: 200 }}>
      <Card>
        <Row text={LONG} />
      </Card>
    </div>
  ),
};

/** No room above, so the tip goes below the anchor rather than off-screen. */
export const NearTheTop: Story = {
  parameters: { layout: "fullscreen" },
  render: () => (
    <div style={{ padding: "var(--space-1)" }}>
      <Card>
        <Row text={LONG} />
      </Card>
    </div>
  ),
};
