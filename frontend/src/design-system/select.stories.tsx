// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type CSSProperties, useState } from "react";
import { Field } from "./atoms";
import { Select, type SelectOption } from "./select";

/**
 * The select, which is a button and a portalled listbox rather than a native
 * `<select>` — the one control a browser draws for itself, in the platform's own
 * idiom, on a screen built entirely from ours.
 *
 * What these stories are for, in order of what they actually catch: the closed
 * face has to sit on the same baseline as a `TextInput` beside it (the Fields
 * story), the popup has to stay unclipped when the control lives in a toolbar
 * inside a scroller (In A Scroller), and it has to flip above the trigger near
 * the bottom of the window (Near The Bottom). Flip the Theme toolbar to see the
 * dark rendering — every value here is a token, so all of it re-resolves.
 */
const meta = {
  title: "Design System/Select",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

type Story = StoryObj<typeof meta>;

const STAGES: readonly SelectOption[] = [
  { value: "qualify", label: "Qualify" },
  { value: "proposal", label: "Proposal" },
  { value: "negotiation", label: "Negotiation" },
  { value: "won", label: "Won" },
  { value: "lost", label: "Lost" },
];

// Long enough that the popup has to scroll rather than grow past the viewport,
// and long enough to hold a real IANA list's worth of near-identical labels.
const ZONES: readonly SelectOption[] = [
  "Europe/Berlin",
  "Europe/Brussels",
  "Europe/Bucharest",
  "Europe/Budapest",
  "Europe/Copenhagen",
  "Europe/Dublin",
  "Europe/Helsinki",
  "Europe/Lisbon",
  "Europe/London",
  "Europe/Madrid",
  "Europe/Oslo",
  "Europe/Paris",
  "Europe/Prague",
  "Europe/Riga",
  "Europe/Rome",
  "Europe/Sofia",
  "Europe/Stockholm",
  "Europe/Tallinn",
  "Europe/Vienna",
  "Europe/Vilnius",
  "Europe/Warsaw",
  "Europe/Zurich",
].map((zone) => ({ value: zone, label: zone }));

const column: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: "var(--space-4)",
  maxWidth: "22rem",
};

/** The control is controlled, so every story owns the value it shows. */
function Demo({
  options,
  start = "",
  label,
  placeholder,
  disabled,
  required,
  hint,
}: Readonly<{
  options: readonly SelectOption[];
  start?: string;
  label: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
  hint?: string;
}>) {
  const [value, setValue] = useState(start);
  return (
    <Field label={label} hint={hint} required={required}>
      {(control) => (
        <Select
          {...control}
          options={options}
          value={value}
          onChange={setValue}
          placeholder={placeholder}
          disabled={disabled}
        />
      )}
    </Field>
  );
}

export const Default: Story = {
  render: () => (
    <div style={column}>
      <Demo label="Stage" options={STAGES} start="proposal" />
    </div>
  ),
};

/** Nothing chosen yet: the face is the placeholder, and it does not read as a value. */
export const WithPlaceholder: Story = {
  render: () => (
    <div style={column}>
      <Demo
        label="Stage"
        options={STAGES}
        placeholder="Pick a stage"
        required
        hint="A deal has to sit somewhere in the pipeline."
      />
    </div>
  ),
};

/** A list past the popup's height cap, which then scrolls inside its own box. */
export const LongList: Story = {
  render: () => (
    <div style={column}>
      <Demo label="Time zone" options={ZONES} start="Europe/Berlin" />
    </div>
  ),
};

export const Disabled: Story = {
  render: () => (
    <div style={column}>
      <Demo label="Stage" options={STAGES} start="won" disabled />
      <Demo
        label="Stage"
        options={STAGES}
        placeholder="Pick a stage"
        disabled
      />
    </div>
  ),
};

/**
 * A choice this workspace cannot make right now stays LISTED and stays readable —
 * that it exists is information — but it takes no hover highlight, the keyboard
 * steps over it, and a click on it does nothing.
 */
export const DisabledOption: Story = {
  render: () => (
    <div style={column}>
      <Demo
        label="Stage"
        start="qualify"
        options={[
          { value: "qualify", label: "Qualify" },
          { value: "proposal", label: "Proposal" },
          { value: "won", label: "Won — needs an approval", disabled: true },
        ]}
      />
    </div>
  ),
};

/**
 * The case the whole positioning design exists for. `.scroll` in app/shell.css is
 * `overflow-y: auto; position: relative`, and most of these controls sit in a
 * toolbar inside it — an absolutely positioned popup is clipped by that box and
 * scrolls away from its own trigger. Open the select, then scroll the frame: the
 * popup stays on the trigger and closes once the trigger is gone.
 */
export const InAScroller: Story = {
  render: () => (
    <div
      style={{
        height: "180px",
        overflowY: "auto",
        border: "1px solid var(--borderSubtle)",
        borderRadius: "var(--r-sm)",
        padding: "var(--space-3)",
        position: "relative",
      }}
    >
      <div style={{ ...column, paddingBottom: "var(--space-6)" }}>
        <Demo label="Stage" options={STAGES} start="proposal" />
        <p className="t-caption">
          Scroll this frame with the list open — the popup follows its trigger
          and is never clipped by this box.
        </p>
        <div style={{ height: "320px" }} />
      </div>
    </div>
  ),
};

/** No room below, so the popup opens upwards instead of off the window. */
export const NearTheBottom: Story = {
  parameters: { layout: "fullscreen" },
  render: () => (
    <div
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "flex-end",
        padding: "var(--space-4)",
      }}
    >
      <div style={column}>
        <Demo label="Time zone" options={ZONES} start="Europe/Vienna" />
      </div>
    </div>
  ),
};

/** The dark rendering, pinned as its own story rather than left to the toolbar. */
export const Dark: Story = {
  globals: { theme: "dark" },
  render: () => (
    <div style={column}>
      <Demo label="Stage" options={STAGES} start="proposal" />
      <Demo label="Time zone" options={ZONES} placeholder="Pick a zone" />
    </div>
  ),
};
