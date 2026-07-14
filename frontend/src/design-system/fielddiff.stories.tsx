import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { FieldDiff, PassportChip } from "./trust";

const meta: Meta = {
  title: "Design System/FieldDiff",
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj;

export const Changed: Story = {
  render: () => (
    <FieldDiff oldValue="Globex Renewal" newValue="Globex Renewal (updated)" />
  ),
};
export const Created: Story = {
  render: () => <FieldDiff oldValue={null} newValue="Carol Wagner" />,
};
export const Cleared: Story = {
  render: () => <FieldDiff oldValue="draft" newValue={null} />,
};
export const Passport: Story = {
  render: () => <PassportChip id="psp_7Q3fa91" />,
};
