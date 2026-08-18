// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { FileChip } from "./filechip";

// A stored file as the card a reader clicks to get it. The two cases that
// matter are on one canvas: a PDF, which is what agreement paper arrives as,
// and everything else, which gets the neutral mark rather than a guessed one.

const meta: Meta<typeof FileChip> = {
  title: "Design system/FileChip",
  component: FileChip,
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj<typeof FileChip>;

export const Pdf: Story = {
  args: { href: "/v1/attachments/a-1", filename: "GR-2026-0092.pdf" },
};

export const OtherKind: Story = {
  args: { href: "/v1/attachments/a-2", filename: "terms-redline.docx" },
};

// Two files on one row, which is the case the control exists for: an amendment
// beside a signed original is tellable apart before the click.
export const TwoOnOneRow: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "var(--space-2)" }}>
      <FileChip href="/v1/attachments/a-1" filename="GR-2026-0092.pdf" />
      <FileChip
        href="/v1/attachments/a-2"
        filename="GR-2026-0092-annex-a.pdf"
      />
    </div>
  ),
};

// A scanner's filename truncates rather than running its row wide.
export const LongName: Story = {
  render: () => (
    <div style={{ maxWidth: 260 }}>
      <FileChip
        href="/v1/attachments/a-3"
        filename="Scan_2026-08-17_Rahmenvertrag_unterschrieben_final.pdf"
      />
    </div>
  ),
};
