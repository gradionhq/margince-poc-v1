// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { LocaleProvider } from "../i18n";
import { Card } from "./atoms";
import { CardBoundary } from "./cardboundary";

// One card's throw, contained. The point of the story is what does NOT happen:
// the cards beside the failed one keep rendering, and so would the navigation
// rail in the real shell.
const client = new QueryClient({
  defaultOptions: { queries: { retry: false } },
});

const meta: Meta<typeof CardBoundary> = {
  title: "Design System/CardBoundary",
  component: CardBoundary,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <Story />
        </LocaleProvider>
      </QueryClientProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj<typeof CardBoundary>;

function Boom(): never {
  throw new Error("cannot read properties of undefined (reading 'rows')");
}

function HealthyCard({ name }: Readonly<{ name: string }>) {
  return (
    <Card title={name}>
      <p className="t-body">Answered, and still here.</p>
    </Card>
  );
}

// Three cards side by side, the middle one throwing during render. Reload the
// story to see the retry put it back — the button clears the query cache as
// well as the boundary's own state, so a card that threw on a failed read gets
// a real second attempt rather than the same error handed straight back.
function ContainedFailure() {
  return (
    <div
      style={{
        display: "grid",
        gap: "var(--space-4)",
        gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))",
      }}
    >
      <CardBoundary>
        <HealthyCard name="Retention" />
      </CardBoundary>
      <CardBoundary>
        <Boom />
      </CardBoundary>
      <CardBoundary>
        <HealthyCard name="Audit trail" />
      </CardBoundary>
    </div>
  );
}

export const OneCardFails: Story = {
  // React reports every error a boundary catches through console.error, so this
  // story — whose whole subject is a caught throw — trips the render gate's
  // console rule by doing exactly what it exists to show. The tag is the gate's
  // declared opt-out (frontend/scripts/fe-uat.mjs), scoped to this one story.
  tags: ["uat-expected-console-error"],
  render: () => <ContainedFailure />,
};
