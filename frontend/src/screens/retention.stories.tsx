// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { RetentionCard } from "./retention";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// Settings → Privacy → Retention. The state worth reviewing here is the one
// the screen exists for: with the retain-only posture ON, an enabled `erase`
// policy is still enabled and does nothing, and the row has to say so beside
// an `archive` row that is unaffected.

const TRANSCRIPTS = {
  id: "00000000-0000-4000-8000-0000000000a1",
  scope: "activity/transcript",
  object_type: "activity",
  category: "transcript",
  retain_days: 365,
  action: "erase",
  lawful_basis: "Art. 9(2)(a)",
  enabled: true,
};

const WON_DEALS = {
  id: "00000000-0000-4000-8000-0000000000a2",
  scope: "deal/won",
  object_type: "deal",
  category: "won",
  retain_days: 2555,
  action: "archive",
  lawful_basis: null,
  enabled: true,
};

const LOST_DEALS = {
  id: "00000000-0000-4000-8000-0000000000a3",
  scope: "deal/lost",
  object_type: "deal",
  category: "lost",
  retain_days: 730,
  action: "anonymize",
  lawful_basis: null,
  enabled: true,
};

// The list as the server renders it: `suppressed_by_posture` is derived from
// the posture, so a fixture that hardcoded it independently of `retainOnly`
// would show a combination the server can never produce.
function policies(
  retainOnly: boolean,
  rows = [TRANSCRIPTS, WON_DEALS, LOST_DEALS],
) {
  return {
    data: rows.map((row) => ({
      ...row,
      suppressed_by_posture: retainOnly && row.action !== "archive",
    })),
    page: { next_cursor: null, has_more: false },
  };
}

// The full authoring grant, and the read-only half of it — the second is what
// draws the posture row's refusal in the answer column, so it needs a story of
// its own rather than a prop nobody exercises.
type RetentionGrant = NonNullable<GrantSpec["retention_policy"]>;
const RETENTION_ADMIN: RetentionGrant = ["read", "create", "update", "delete"];
const RETENTION_READER: RetentionGrant = ["read"];

function retention(
  retainOnly: boolean,
  extra: RouteMap = {},
  allow: RetentionGrant = RETENTION_ADMIN,
) {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { retention_policy: allow } })),
      "GET /retention/settings": () =>
        jsonResponse({ retain_only: retainOnly }),
      "GET /retention-policies": () => jsonResponse(policies(retainOnly)),
      ...extra,
    });
    return (
      <StoryProviders>
        <RetentionCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof RetentionCard> = {
  title: "Settings/Admin settings/Privacy/Retention",
  component: RetentionCard,
};
export default meta;

type Story = StoryObj<typeof RetentionCard>;

// The posture off: every policy acts as authored.
export const LadderActing: Story = { render: retention(false) };

// The posture on: the erase and anonymize rows are enabled and inert, and each
// says why; the archive row is untouched because archiving retains.
export const RetainOnly: Story = { render: retention(true) };

// A seat that may read the ladder and change none of it: the posture switch is
// refused with the sentence that says why, and both sit in the row's answer
// column against its right edge. The `Add policy` verb is absent from the header
// without the create grant, so the card's title stands alone.
export const ReadOnlyPosture: Story = {
  render: retention(true, {}, RETENTION_READER),
};

// The same page in dark, because the two things this screen says are both said
// in colour-adjacent ways. The posture Switch is ON, so its track carries the
// accent fill against `--bgElevated` and the thumb has to stay visible on it —
// the one control in the settings tree whose state is a shape rather than a word.
// And the suppressed rows say "enabled and inert" with a `--textMeta` caption
// beside a badge, which is the contrast pair most likely to collapse when the
// ground goes dark.
export const RetainOnlyDark: Story = {
  globals: { theme: "dark" },
  render: retention(true),
};

// The inline authoring form, defaulting to the least destructive action.
export const CreateForm: Story = {
  render: retention(false),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /add policy/i }),
    );
  },
};

// The inline editor: window, action and basis, plus the Enabled switch that
// pauses a rule without losing it.
export const RowEditor: Story = {
  render: retention(true),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const row = await canvas.findByTestId("retention-row-deal/won");
    await userEvent.click(within(row).getByRole("button", { name: /edit/i }));
  },
};

// A duplicate scope is a 409 with exactly one cause, so the refusal names the
// row to edit rather than relaying the constraint.
export const DuplicateScope: Story = {
  render: retention(false, {
    "POST /retention-policies": () =>
      jsonResponse(
        {
          type: "https://errors.gradion.com/conflict",
          title: "Conflict",
          status: 409,
          code: "conflict",
          detail:
            'retention policy for scope "activity/transcript" already exists: conflict',
        },
        409,
      ),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /add policy/i }),
    );
    await userEvent.type(
      await canvas.findByLabelText(/window in days/i),
      "365",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: /create policy/i }),
    );
    await canvas.findByRole("alert");
  },
};

// Nothing ages out at all — the honest empty state for an installation whose
// policies were all deleted.
export const Empty: Story = {
  render: retention(false, {
    "GET /retention-policies": () =>
      jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
  }),
};
