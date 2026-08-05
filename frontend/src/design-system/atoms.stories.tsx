import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  Avatar,
  Badge,
  Button,
  Checkbox,
  Radio,
  Select,
  Textarea,
  TextInput,
} from "./atoms";

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

// The four field controls in one column, which is the point of the story: a
// text input, a dropdown and a textarea stacked the way a form stacks them is
// the only way to see that their type size, padding, height and focus ring
// actually agree. Reviewed one at a time they always look fine.
export const Fields: Story = {
  render: () => (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <label className="field">
        <span className="t-label">Deal name</span>
        <TextInput defaultValue="Globex renewal" />
      </label>
      <label className="field">
        <span className="t-label">Stage</span>
        <Select defaultValue="proposal">
          <option value="qualify">Qualify</option>
          <option value="proposal">Proposal</option>
          <option value="won">Won</option>
        </Select>
      </label>
      <label className="field">
        <span className="t-label">Note</span>
        <Textarea rows={3} defaultValue="Renewal terms agreed on the call." />
      </label>
    </div>
  ),
};

// A disabled control and a long label that wraps — the two states a field
// catalog usually omits and a real form always reaches.
export const Toggles: Story = {
  render: () => (
    <div className="form-stack" style={{ maxWidth: "22rem" }}>
      <Checkbox label="Replace the existing link" defaultChecked />
      <Checkbox label="Include archived records" />
      <Checkbox label="Notify the deal owner" disabled />
      <fieldset className="field-multiselect">
        <legend className="t-label">Target</legend>
        <Radio name="quota-side" label="Owner" defaultChecked />
        <Radio name="quota-side" label="Team" />
      </fieldset>
    </div>
  ),
};
