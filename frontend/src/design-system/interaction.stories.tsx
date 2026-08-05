// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type CSSProperties, useEffect, useRef } from "react";

/**
 * The interaction colours the BROWSER owns, catalogued because they are the one
 * part of the palette no component declares.
 *
 * The text cursor, the tick in a checkbox, the scrollbar thumb and the selection
 * highlight are all painted by the engine, and left alone they are the platform's
 * blue — the last place a themed product shows someone else's brand. They are set
 * once at the document root in `app.css`, and every one of those properties
 * inherits, which is why there is nothing per-component to show here and why this
 * file renders bare elements rather than atoms.
 *
 * Flip the theme in the toolbar: each value is a token, so all of it re-resolves.
 * The selection pair was checked for contrast rather than eyeballed — --bgPage on
 * --accent is 5.21:1 in light and 5.37:1 in dark.
 */
const meta = {
  title: "Design System/Interaction Colours",
  parameters: { layout: "centered" },
} satisfies Meta;
export default meta;

type Story = StoryObj<typeof meta>;

/** Enough rows to overflow the box; the text IS the key, so no index keys. */
const SCROLL_LINES = Array.from(
  { length: 20 },
  (_, index) => `Line ${index + 1} — scroll this box.`,
);

const column: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: "var(--space-4)",
  minWidth: "320px",
};
const label: CSSProperties = {
  fontSize: "11px",
  letterSpacing: "0.08em",
  textTransform: "uppercase",
  color: "var(--textMeta)",
};
const field: CSSProperties = {
  padding: "var(--space-2)",
  border: "1px solid var(--borderControl)",
  borderRadius: "var(--r-sm)",
  background: "var(--bgElevated)",
  color: "var(--textPrimary)",
  font: "inherit",
};

/**
 * The caret. Focus either field to see it: brand accent, and a BLOCK rather than a
 * bar — which is worth looking at directly, because a block caret is opaque and
 * covers the glyph at the insertion point while it is on. Chromium only for now;
 * other engines keep the bar and the colour.
 */
export const TextCursor: Story = {
  render: () => (
    <div style={column}>
      <span style={label}>caret-color + caret-shape</span>
      <input style={field} defaultValue="Click here and move the cursor" />
      <textarea
        style={field}
        rows={3}
        defaultValue={"Two lines,\nsame caret."}
      />
    </div>
  ),
};

/**
 * `accent-color`, which is the only way to theme a native control's own paint.
 * These four used to carry the declaration locally in three screens; the root
 * rule made those redundant, and this story is where that claim is visible —
 * nothing below sets a colour of its own.
 */
export const NativeControls: Story = {
  render: () => (
    <div style={column}>
      <span style={label}>accent-color</span>
      <label style={{ display: "flex", gap: "var(--space-2)" }}>
        <input type="checkbox" defaultChecked /> Checked
      </label>
      <label style={{ display: "flex", gap: "var(--space-2)" }}>
        <input type="radio" defaultChecked /> Selected
      </label>
      <input type="range" defaultValue={62} />
      <progress value={0.62} />
    </div>
  ),
};

/**
 * The scrollbar: thin, accent thumb, and a TRANSPARENT track. The track colour is
 * the interesting decision — a scroller is not always on the page surface (the
 * rail is a dark green field in both themes, a code block has its own fill), so
 * naming a colour would be wrong for every scroller that sits somewhere else.
 */
export const Scrollbars: Story = {
  render: () => (
    <div style={column}>
      <span style={label}>scrollbar-width + scrollbar-color</span>
      <div
        style={{
          ...field,
          height: "160px",
          overflow: "auto",
          padding: "var(--space-3)",
        }}
      >
        {SCROLL_LINES.map((line) => (
          <p key={line} style={{ margin: 0 }}>
            {line}
          </p>
        ))}
      </div>
    </div>
  ),
};

/**
 * Selection, pre-selected on mount so the story shows the state rather than
 * asking to be dragged. Rendered twice on purpose: body copy and a form control,
 * because a selection inside an input is the same selection and some engines do
 * not inherit the pseudo-element into one.
 */
export const Selection: Story = {
  render: () => {
    const paragraph = useRef<HTMLParagraphElement>(null);
    useEffect(() => {
      const node = paragraph.current;
      if (!node) {
        return;
      }
      const range = document.createRange();
      range.selectNodeContents(node);
      const selection = window.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
    }, []);
    return (
      <div style={column}>
        <span style={label}>::selection</span>
        <p ref={paragraph} style={{ margin: 0, color: "var(--textContent)" }}>
          Selected text is the accent holding it.
        </p>
        <input style={field} defaultValue="Select inside a field too" />
      </div>
    );
  },
};
