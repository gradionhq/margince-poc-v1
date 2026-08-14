// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LocaleProvider } from "../i18n";
import { Card } from "./atoms";
import { type SectionState, SurfaceState } from "./surfacestate";

// The nine states a surface can be in. They are drawn together because that is
// the only way to judge the thing they exist for: an empty card and a withheld
// card make the same shape on screen and mean opposite things, and the words
// are all that separate them.
const meta: Meta<typeof SurfaceState> = {
  title: "Design System/SurfaceState",
  component: SurfaceState,
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

type Story = StoryObj<typeof SurfaceState>;

const ROWS = (
  <ul className="t-body" style={{ listStyle: "none" }}>
    <li>Renewal — €48,000</li>
    <li>Expansion, EU — €12,500</li>
  </ul>
);

const ALL: readonly SectionState[] = [
  "ready",
  "empty",
  "withheld",
  "unavailable",
  "loading",
  "unsupported",
  "failed",
  "stale",
  "partial",
];

const DETAIL = {
  onRetry: () => undefined,
  staleAsOf: "9:15 this morning",
  remaining: 4,
};

function AllStates() {
  return (
    <div
      style={{
        display: "grid",
        gap: "var(--space-4)",
        gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))",
      }}
    >
      {ALL.map((state) => (
        <Card key={state} title={state}>
          <SurfaceState
            state={state}
            emptyLabel="No open deal on this account."
            detail={DETAIL}
          >
            {ROWS}
          </SurfaceState>
        </Card>
      ))}
    </div>
  );
}

export const EveryState: Story = { render: () => <AllStates /> };

/** `stale` puts the caveat ABOVE the rows and `partial` puts the count BELOW
 * them, and neither is a layout preference: a caveat under a figure arrives
 * after the reader has already taken it as current, while a truncation count
 * above a list describes something they have not read yet. */
function OrderDemo() {
  return (
    <div style={{ display: "grid", gap: "var(--space-4)", maxWidth: 420 }}>
      <Card title="Stale — caveat first">
        <SurfaceState
          state="stale"
          emptyLabel="No open deal on this account."
          detail={{ staleAsOf: "9:15 this morning" }}
        >
          {ROWS}
        </SurfaceState>
      </Card>
      <Card title="Partial — count last">
        <SurfaceState
          state="partial"
          emptyLabel="No open deal on this account."
          detail={{ remaining: 4 }}
        >
          {ROWS}
        </SurfaceState>
      </Card>
    </div>
  );
}

export const CaveatOrder: Story = { render: () => <OrderDemo /> };

/** A `failed` state with no `onRetry` is `unavailable` with extra words, so it
 * draws the sentence and no button — the retry is what makes the state
 * different, not the wording. */
function RetryDemo() {
  return (
    <div style={{ display: "grid", gap: "var(--space-4)", maxWidth: 420 }}>
      <Card title="Failed, retryable">
        <SurfaceState
          state="failed"
          emptyLabel="Nothing recorded."
          detail={{ onRetry: () => undefined }}
        >
          {ROWS}
        </SurfaceState>
      </Card>
      <Card title="Failed, nothing to retry">
        <SurfaceState state="failed" emptyLabel="Nothing recorded.">
          {ROWS}
        </SurfaceState>
      </Card>
    </div>
  );
}

export const FailedWithAndWithoutRetry: Story = {
  render: () => <RetryDemo />,
};

/** Two independently-governed parts in one card. Each is named, so "hidden
 * from you" attaches to a named thing rather than floating under a heading
 * that covers both. */
function LabelledDemo() {
  return (
    <Card title="Tags and lists" style={{ maxWidth: 420 }}>
      <SurfaceState label="Lists" state="ready" emptyLabel="Not on any list.">
        <p className="t-body">Q3 expansion targets</p>
      </SurfaceState>
      <SurfaceState label="Tags" state="withheld" emptyLabel="No tags.">
        <p className="t-body">strategic</p>
      </SurfaceState>
    </Card>
  );
}

export const NamedParts: Story = { render: () => <LabelledDemo /> };
