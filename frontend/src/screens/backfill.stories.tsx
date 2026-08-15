// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { BackfillPanel } from "./backfill";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// BackfillPanel stories for the fe-uat render gate — one per honest branch
// the panel now covers: the ordinary setup/running/done/error/cancelled run
// (seeded via `initial`, matching how connectors.tsx now mounts this panel
// off the embedded CaptureConnection.backfill), plus the three added by this
// change — a provider with no Backfiller, a stalled running run, and a
// refused window narrowing.

type BackfillStatus = components["schemas"]["BackfillStatus"];
type Provider = components["schemas"]["CaptureConnection"]["provider"];

function panelStory(
  provider: Provider,
  initial: BackfillStatus,
  routes: Record<string, (body: unknown) => Response> = {},
) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <BackfillPanel provider={provider} initial={initial} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof BackfillPanel> = {
  title: "Settings/You/Connections/Backfill",
  component: BackfillPanel,
};
export default meta;
type Story = StoryObj<typeof BackfillPanel>;

export const Setup: Story = {
  render: panelStory(
    "gmail",
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "6m",
          estimated_messages: 1234,
          computed_at: "2026-07-23T10:00:00Z",
        }),
    },
  ),
};

// A run that moved a moment ago, so it keeps the progress bar rather than the
// staleness note. Shared by the variants below, which need this same live state.
const RUNNING: BackfillStatus = {
  state: "running",
  backfill_id: "018f3a1b-0000-7000-8000-0000000000b1",
  window: "6m",
  estimated_messages: 400,
  counts: {
    captured: 128,
    people_created: 47,
    organizations_created: 12,
    messages_scanned: 150,
  },
  updated_at: new Date().toISOString(),
};

const DONE: BackfillStatus = {
  state: "done",
  counts: {
    captured: 512,
    people_created: 90,
    organizations_created: 20,
    messages_scanned: 600,
  },
};

export const Running: Story = { render: panelStory("gmail", RUNNING) };

export const Done: Story = { render: panelStory("gmail", DONE) };

// The live run in dark. The bar is a bare `<progress>` element with nothing but
// a width in the sheet, so its track and fill are whatever the browser paints —
// the one control on these screens that does not get its colour from a token and
// therefore cannot be reasoned about from the CSS. The stat values are `--accent`
// on the panel ground beside it.
export const RunningDark: Story = {
  globals: { theme: "dark" },
  render: panelStory("gmail", RUNNING),
};

// The finished run in dark. `.capture-hero.done` is the one tinted plate in this
// panel — `--successBg` behind the text with an `--online` border and a
// `--success` glyph — and "arrival" is the whole message of the state. A tint
// that composites to the same value as the card leaves a hero saying nothing.
export const DoneDark: Story = {
  globals: { theme: "dark" },
  render: panelStory("gmail", DONE),
};

// The live run at 390px. The counts sit in a two-column grid with an 84px floor
// per stat and wrap outside it, and the progress bar takes the full width — this
// is where a four-stat grid either becomes two rows or overflows the card.
export const RunningPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: panelStory("gmail", RUNNING),
};

export const ErrorState: Story = {
  render: panelStory("gmail", {
    state: "error",
    counts: { captured: 40, people_created: 9 },
    last_error_class: "auth",
  }),
};

export const Cancelled: Story = {
  render: panelStory("gmail", {
    state: "cancelled",
    counts: { captured: 20, messages_scanned: 40 },
  }),
};

// A running run whose updated_at hasn't moved in 20 minutes: the progress
// bar stops claiming motion and the panel says when it last actually moved.
export const Stale: Story = {
  render: panelStory("gmail", {
    state: "running",
    estimated_messages: 400,
    counts: { captured: 40, messages_scanned: 40 },
    updated_at: new Date(Date.now() - 20 * 60_000).toISOString(),
  }),
};

// No provider-side message count to divide by: absolute counts only, never
// a percentage over a guess.
export const NullEstimate: Story = {
  render: panelStory("gmail", {
    state: "running",
    estimated_messages: null,
    counts: { captured: 12 },
  }),
};

// IMAP has no Backfiller — the setup screen's auto-preview hits
// connector_unsupported and the panel states the capability gap plainly
// instead of rendering a window picker that can only fail again.
export const Unsupported: Story = {
  render: panelStory(
    "imap",
    { state: "none" },
    {
      "POST /connectors/imap/backfill/preview": () =>
        jsonResponse({ code: "connector_unsupported" }, 422),
    },
  ),
};

// A prior wider run already covers more history than the newly-picked
// window would — start refuses with window_narrowing, and the panel
// explains the widen-only rule rather than a generic failure.
export const Narrowing: Story = {
  render: panelStory(
    "gmail",
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "3m",
          estimated_messages: 200,
          computed_at: "2026-07-23T10:00:00Z",
        }),
      "POST /connectors/gmail/backfill": () =>
        jsonResponse({ code: "window_narrowing" }, 409),
    },
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /Start the import/ }),
    );
    await canvas.findByText(/only be widened/i);
  },
};
