// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { LocaleProvider } from "../i18n";
import { RichText } from "./richtext";

const meta: Meta<typeof RichText> = {
  title: "Design System/RichText",
  component: RichText,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj<typeof RichText>;

/** Both renderings, side by side — because both go on the wire. */
function Demo({ initial }: Readonly<{ initial: string }>) {
  const [value, setValue] = useState(initial);
  const [text, setText] = useState("");
  return (
    <div style={{ display: "grid", gap: "var(--space-3)" }}>
      <RichText
        value={value}
        onChange={(next) => {
          setValue(next.html);
          setText(next.text);
        }}
        label="Message"
        labels={{
          bold: "Bold",
          italic: "Italic",
          bulletList: "Bulleted list",
          numberList: "Numbered list",
          link: "Link",
          linkPrompt: "Web address for this link",
        }}
        placeholder="Write your message…"
        rows={6}
      />
      <div className="t-caption">
        The plain alternative a text client receives:
        <pre style={{ whiteSpace: "pre-wrap", margin: "var(--space-1) 0 0" }}>
          {text || "—"}
        </pre>
      </div>
    </div>
  );
}

/** Empty, showing the placeholder. */
export const Empty: Story = { render: () => <Demo initial="" /> };

/** The formatting the toolbar produces, which is all the wire carries. */
export const Formatted: Story = {
  render: () => (
    <Demo
      initial={
        "<p>Hallo Marine,</p>" +
        "<p>the <b>deadline</b> is <em>Friday</em>. Two things before then:</p>" +
        "<ul><li>the signed scope</li><li>the depot list</li></ul>" +
        '<p>Details on <a href="https://gradion.com">our page</a>.</p>'
      }
    />
  ),
};

/**
 * An AI draft arriving with markup the allowlist refuses — a tracking pixel and
 * a script. Neither reaches the editor, because a model's output is untrusted
 * input however friendly its source, and the words around them survive.
 */
export const HostileDraft: Story = {
  render: () => (
    <Demo
      initial={
        "<p>Real words.</p>" +
        '<img src="https://track.test/o.gif">' +
        "<script>alert(1)</script>" +
        "<div>kept, unwrapped</div>"
      }
    />
  ),
};
