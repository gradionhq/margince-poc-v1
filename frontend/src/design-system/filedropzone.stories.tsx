// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { fireEvent, within } from "storybook/test";
import { LocaleProvider } from "../i18n";
import { FileDropzone } from "./filedropzone";

const meta: Meta = {
  title: "Design System/FileDropzone",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <div style={{ maxWidth: "480px" }}>
          <Story />
        </div>
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj;

/** Nothing chosen yet: the zone is asking, and says what it will accept. */
export const Empty: Story = {
  render: () => (
    <FileDropzone
      label="Document"
      hint="PDF, Word or plain text, up to 25 MB."
      emptyLabel="Drop the file here, or click to choose one"
      onPick={() => {}}
    />
  ),
};

/** A file chosen: the label becomes the answer, so it takes the content colour
 * rather than staying placeholder grey. */
export const Chosen: Story = {
  render: () => (
    <FileDropzone
      label="Document"
      hint="PDF, Word or plain text, up to 25 MB."
      emptyLabel="Drop the file here, or click to choose one"
      file={new File(["order form"], "order_form.txt", { type: "text/plain" })}
      onPick={() => {}}
    />
  ),
};

/** Holding a file over the zone. The state is driven rather than posed: `over`
 * is the component's own, and a prop that forced it would be API existing only
 * for this story. It needs to be SEEN because the jsdom test can only prove the
 * class is toggled — the screen-local zone this replaced toggled the same class
 * with no rule behind it anywhere, and no test at that level could tell. */
export const Dragover: Story = {
  render: () => (
    <FileDropzone
      label="Document"
      hint="PDF, Word or plain text, up to 25 MB."
      emptyLabel="Drop the file here, or click to choose one"
      onPick={() => {}}
    />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    // The drag handlers live on the input, which covers the whole zone.
    await fireEvent.dragOver(await canvas.findByLabelText("Document"), {
      dataTransfer: { files: [] },
    });
  },
};

/** Live, so the hover and focus rings can be judged against the dragover state
 * they share a border colour with. */
export const Interactive: Story = {
  render: function Interactive() {
    const [file, setFile] = useState<File | undefined>();
    return (
      <FileDropzone
        label="Document"
        hint="Choosing a second file replaces the first."
        emptyLabel="Drop the file here, or click to choose one"
        file={file}
        onPick={setFile}
      />
    );
  },
};
