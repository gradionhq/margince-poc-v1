import type { Meta, StoryObj } from "@storybook/react-vite";
import { Avatar, Badge, Button } from "./atoms";

// Stories are the render surface the change-scoped fe-uat capture gate drives
// (frontend/scripts/fe-uat.mjs): a change to atoms.tsx re-renders these in a
// headless browser and fails on an unclean render. One story file per
// component module — fe-uat maps atoms.tsx → atoms.stories.tsx.
const meta: Meta = {
  title: "Design System/Atoms",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

export const Buttons: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Button variant="primary">Save</Button>
      <Button variant="ghost">Cancel</Button>
      <Button variant="danger">Delete</Button>
      <Button variant="primary" small>
        Small
      </Button>
    </div>
  ),
};

export const Badges: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Badge tone="success">Active</Badge>
      <Badge tone="warn">Pending</Badge>
      <Badge tone="danger">Overdue</Badge>
      <Badge tone="ai">AI</Badge>
      <Badge tone="accent">Rep</Badge>
    </div>
  ),
};

export const Avatars: Story = {
  render: () => (
    <div style={{ display: "flex", gap: "0.75rem", alignItems: "center" }}>
      <Avatar name="Alice Müller" />
      <Avatar name="Bob Schmidt" />
      <Avatar name="Carol Wagner" />
    </div>
  ),
};
