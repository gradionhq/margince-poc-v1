import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { MoneyInput } from "./moneyinput";

const meta: Meta = {
  title: "Design System/MoneyInput",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

function MoneyDemo() {
  const [minor, setMinor] = useState(150_000);
  return (
    <div style={{ maxWidth: 200 }}>
      <MoneyInput
        valueMinor={minor}
        currency="EUR"
        onChangeMinor={setMinor}
        aria-label="Unit price"
      />
      <p style={{ marginTop: 8 }}>minor units: {minor}</p>
    </div>
  );
}

export const Default: Story = {
  render: () => <MoneyDemo />,
};
