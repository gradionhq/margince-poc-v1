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

/** Waiting on the user. Brand colour, quicker pulse — nothing claimed yet. */
export const Listening: Story = { args: { state: "listening" } };

export const Working: Story = { args: { state: "working" } };

/**
 * The ring is the optional half of WDS-CORE-2, and a ring rather than a bar
 * because the Core is already the thing being waited on.
 */
export const WorkingWithProgress: Story = {
  args: { state: "working", progress: 0.58 },
};

export const Success: Story = { args: { state: "success" } };

/** Needs a human. Amber, and the only warm state in the vocabulary. */
export const Attention: Story = { args: { state: "attention" } };

export const Errored: Story = { args: { state: "error" } };

/**
 * Present, not working. Desaturated rather than dimmed, so it does not read as a
 * disabled control.
 */
export const Quiet: Story = { args: { state: "quiet" } };

/**
 * The one state that does not breathe: a server we cannot reach must not look
 * busy, and a frozen frame says that better than any colour.
 */
export const Unavailable: Story = { args: { state: "unavailable" } };

/** The workbench header's size, set through the documented custom properties. */
export const SmallWithoutFeed: Story = {
  args: { state: "idle", size: "md", feed: false },
};
