// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { type CSSProperties, useState } from "react";
import { Field } from "./atoms";
import { DateInput } from "./dateinput";
import { TokenInput, TokenList } from "./tokeninput";

/**
 * The two value controls a typed filter clause needs and the design system did
 * not have: a set of short values for the `in` operator, and a date for the
 * ordered comparisons.
 *
 * What these stories are for, in order of what they catch: the token frame has to
 * GROW as tokens wrap (it reads `.input`, which is a single-line height, so the
 * override is load-bearing and a regression is invisible until the second row
 * clips), the token and the date box have to sit on the same baseline as a
 * `TextInput` beside them, and both have to read correctly in dark — every value
 * here is a token, so all of it re-resolves.
 */
const meta = {
  title: "Design System/Value inputs",
  parameters: { layout: "padded" },
} satisfies Meta;
export default meta;

type Story = StoryObj<typeof meta>;

const column: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: "var(--space-4)",
  maxWidth: "26rem",
};

/** Controlled, so each story owns the values it shows. */
function TokenDemo({
  start = [],
  label,
  placeholder,
  disabled,
  hint,
}: Readonly<{
  start?: readonly string[];
  label: string;
  placeholder?: string;
  disabled?: boolean;
  hint?: string;
}>) {
  const [values, setValues] = useState<readonly string[]>(start);
  return (
    <Field label={label} hint={hint}>
      {(control) => (
        <TokenInput
          {...control}
          values={values}
          onChange={setValues}
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
      <TokenDemo
        label="Region is any of"
        start={["DE", "AT", "CH"]}
        hint="Enter or comma commits a value; Backspace on an empty box removes the last."
      />
    </div>
  ),
};

/** Nothing entered yet: the placeholder shows, and hides once a token exists. */
export const Empty: Story = {
  render: () => (
    <div style={column}>
      <TokenDemo label="Region is any of" placeholder="DE, AT, CH" />
    </div>
  ),
};

/**
 * Enough tokens to wrap. This is the case the frame's `height: auto` exists for:
 * `.input` declares a single-line height, so without the override the second row
 * is clipped and the tokens a reader added appear to have vanished.
 */
export const Wrapping: Story = {
  render: () => (
    <div style={column}>
      <TokenDemo
        label="Industry is any of"
        start={[
          "Automotive",
          "Mechanical engineering",
          "Logistics",
          "Pharmaceuticals",
          "Renewables",
          "Construction",
        ]}
      />
    </div>
  ),
};

export const Disabled: Story = {
  render: () => (
    <div style={column}>
      <TokenDemo label="Region is any of" start={["DE", "AT"]} disabled />
    </div>
  ),
};

/**
 * The date control beside a token input and a plain field, which is how a clause
 * row actually renders them — the three closed faces have to agree on height and
 * baseline or the row reads as three different fields.
 */
export const DateBesideTokens: Story = {
  render: () => (
    <div style={column}>
      <Field label="Last emailed before">
        {(control) => <DateInput {...control} defaultValue="2026-07-18" />}
      </Field>
      <TokenDemo label="Region is any of" start={["DE", "AT"]} />
    </div>
  ),
};

/** The dark rendering, pinned as its own story rather than left to the toolbar. */
export const Dark: Story = {
  globals: { theme: "dark" },
  render: () => (
    <div style={column}>
      <TokenDemo label="Region is any of" start={["DE", "AT", "CH"]} />
      <Field label="Last emailed before">
        {(control) => <DateInput {...control} defaultValue="2026-07-18" />}
      </Field>
    </div>
  ),
};

/**
 * The set WITHOUT the text box — the shape a picker fills. Two states on one
 * canvas, because they are the two a caller ships: a set somebody can edit, and
 * the same set shown to a reader who may only look at it.
 */
export const ListWithoutAField: Story = {
  render: () => (
    <div style={column}>
      <TokenList
        items={[
          { id: "8801", label: "Chi Mai" },
          { id: "8802", label: "Anh Tuan" },
          { id: "8803", label: "Nguyễn Bá Dũng" },
        ]}
        removeLabel={(item) => `Take ${item.label} off the list`}
        onRemove={() => {}}
      />
      <TokenList
        items={[{ id: "8801", label: "Chi Mai" }]}
        removeLabel={(item) => `Take ${item.label} off the list`}
      />
    </div>
  ),
};
