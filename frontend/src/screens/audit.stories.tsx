// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { AuditEntryLine } from "./audit";
import { StoryProviders } from "./story-utils";

// ActorTag / AuditEntryLine: the shared audit attribution line, rendered by
// every audit surface (the settings compliance log, the custom-fields change
// rail). What these stories exist to show is WHO a row is attributed to —
// the person first, the machine as a qualifier on them (PD-002). A change here
// is a change to what an auditor reads before anything else, so every
// attribution state gets a row rather than only the happy one.
const meta: Meta = {
  title: "Compliance/Audit attribution",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type AuditLogEntry = components["schemas"]["AuditLogEntry"];

// The viewer's own id, as /me reports it: BARE. The wire spells a human actor
// "human:<uuid>", so the two are different strings — the difference ActorTag
// owns, and the reason "You" silently never rendered before it did.
const ME = "01a01740-c9c2-736d-a0b6-d3e3dcb13111";

const row = (over: Partial<AuditLogEntry>): AuditLogEntry => ({
  id: crypto.randomUUID(),
  actor_type: "human",
  actor_id: `human:${ME}`,
  action: "update",
  entity_type: "deal",
  entity_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13222",
  occurred_at: "2026-07-10T14:09:00Z",
  ...over,
});

// Every attribution state the read path can produce, in one frame, so a
// reviewer can compare them instead of clicking between screens.
const STATES: ReadonlyArray<{ caption: string; entry: AuditLogEntry }> = [
  {
    caption:
      "The viewer's own change — reads “You”, not their name back at them",
    entry: row({ action: "create" }),
  },
  {
    caption: "Another person — NAMED, where this used to read “A teammate”",
    entry: row({ actor_id: "human:u-lars", actor_name: "Lars Vogt" }),
  },
  {
    caption:
      "A member whose user row no longer resolves — honest, never an invented name or a raw uuid",
    entry: row({ actor_id: "human:u-gone", actor_name: null }),
  },
  {
    caption:
      "An agent under a human's authority — the PERSON is the label, the tool the qualifier",
    entry: row({
      actor_type: "agent",
      actor_id: "agent:01a01740-c9c2-736d-a0b6-d3e3dcb13333",
      passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13999",
      on_behalf_of: "u-lars",
      on_behalf_of_name: "Lars Vogt",
    }),
  },
  {
    caption:
      "An agent acting under the VIEWER's authority — “You, via an agent”",
    entry: row({
      actor_type: "agent",
      actor_id: "agent:p2",
      passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13888",
      on_behalf_of: ME,
      on_behalf_of_name: "Ada Mortensen",
    }),
  },
  {
    caption:
      "A grant was presented and no human resolved — a GAP, and it says so rather than reading “System”",
    entry: row({
      actor_type: "agent",
      actor_id: "agent:scheduled_send",
      passport_id: "01a01740-c9c2-736d-a0b6-d3e3dcb13aaa",
      action: "send_email",
    }),
  },
  {
    caption:
      "No grant presented — a background pass nobody's context ran, so no gap to report",
    entry: row({ actor_type: "agent", actor_id: "agent:org_name_promotion" }),
  },
  {
    caption:
      "A connector a person authorised — the same rule as an agent, keyed on the grant",
    entry: row({
      actor_type: "connector",
      actor_id: "connector:gmail",
      on_behalf_of: "u-lars",
      on_behalf_of_name: "Lars Vogt",
      action: "import",
    }),
  },
  {
    caption:
      "A bare connector — no connect flow, so no granting human BY DESIGN and no gap",
    entry: row({
      actor_type: "connector",
      actor_id: "connector:finance",
      action: "import",
    }),
  },
  {
    caption: "Genuinely nobody behind it — the one case that reads “System”",
    entry: row({ actor_type: "system", actor_id: "system", action: "erase" }),
  },
];

function AttributionGallery({
  locale,
}: Readonly<{ locale?: "en" | "de" | "vi" }>) {
  return (
    <StoryProviders locale={locale}>
      <div
        style={{ display: "grid", gap: "var(--space-5)", maxWidth: "720px" }}
      >
        {STATES.map(({ caption, entry }) => (
          <div key={entry.id}>
            <p
              className="t-small"
              style={{ marginBottom: "var(--space-1)", opacity: 0.7 }}
            >
              {caption}
            </p>
            <AuditEntryLine entry={entry} meUserId={ME} />
          </div>
        ))}
      </div>
    </StoryProviders>
  );
}

export const EveryAttributionState: Story = {
  render: () => <AttributionGallery />,
};

// The qualifiers and the gap phrase are localised strings, not English the code
// happens to emit — a locale that fell back to English would show it here.
export const German: Story = {
  render: () => <AttributionGallery locale="de" />,
};

export const Vietnamese: Story = {
  render: () => <AttributionGallery locale="vi" />,
};
