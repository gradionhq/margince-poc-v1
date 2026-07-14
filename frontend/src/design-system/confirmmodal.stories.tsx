import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { ConfirmModal } from "./confirmmodal";

const meta: Meta = {
  title: "Design System/ConfirmModal",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

function ConfirmDemo({
  tier,
  error,
}: Readonly<{ tier?: "confirm"; error?: string | null }>) {
  const [open, setOpen] = useState(true);
  return (
    <ConfirmModal
      open={open}
      onClose={() => setOpen(false)}
      title="Send this offer?"
      tier={tier}
      confirmLabel="Send"
      onConfirm={() => setOpen(false)}
      error={error}
    >
      <p>The buyer will receive the offer by email.</p>
    </ConfirmModal>
  );
}

export const Default: Story = {
  render: () => <ConfirmDemo />,
};

export const ConfirmTier: Story = {
  render: () => <ConfirmDemo tier="confirm" />,
};

export const WithError: Story = {
  render: () => <ConfirmDemo error="A currency conversion rate is missing." />,
};
