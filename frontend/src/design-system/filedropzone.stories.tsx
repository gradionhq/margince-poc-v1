// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
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
