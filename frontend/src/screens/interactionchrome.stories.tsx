// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { interactionIcon, useInteractionLabel } from "./interactionchrome";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

/**
 * How a captured interaction is drawn and named.
 *
 * The KIND and the TRANSPORT are separate axes (ADR-0107/A158), and reading one
 * off the other is what drew an envelope beside every chat message on the person
 * page — on contacts who have no email address at all. So the icon comes from
 * the kind, the name comes from the installation's transport directory, and
 * neither is inferred from the other.
 *
 * The catalog below is the whole vocabulary at once, which is what makes the
 * defect visible: a row whose icon disagrees with its name is a row claiming a
 * transport the record never carried.
 */
const meta: Meta = {
  title: "Records/Person record/Interaction chrome",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

// One row per activity kind the contract declares, plus the two message rows
// that differ only by which transport carried them — the pair a reader has to be
// able to tell apart, and the pair the old envelope collapsed into one.
const CATALOG: ReadonlyArray<{ kind: string; provider?: string }> = [
  { kind: "email" },
  { kind: "call" },
  { kind: "meeting" },
  { kind: "note" },
  { kind: "task" },
  { kind: "message", provider: "zalo_oa" },
  { kind: "message", provider: "dispact" },
  // A transport this build has never heard of: an extension unit creates
  // exactly this case, and the directory falls back to the raw id rather than
  // blanking the cell.
  { kind: "message", provider: "telegram" },
  // A kind this build has never heard of. It is drawn as a plain record,
  // because any other icon would be a guess about a transport.
  { kind: "webinar" },
];

function Catalog() {
  const interactionLabel = useInteractionLabel();
  return (
    <div className="pe-chiprow">
      {CATALOG.map((entry) => (
        <span
          className="pe-memory-channel"
          key={`${entry.kind}-${entry.provider ?? "none"}`}
        >
          {interactionIcon(entry.kind)}
          {interactionLabel(entry.kind, entry.provider)}
        </span>
      ))}
    </div>
  );
}

export const EveryKind: Story = {
  render: () => {
    installFetchStub({
      "GET /channel-providers": () =>
        jsonResponse({
          data: [
            {
              provider: "zalo_oa",
              label: "Zalo OA",
              credential_model: "workspace_bot",
              supplies_transport: true,
            },
            {
              provider: "dispact",
              label: "Dispact",
              credential_model: "per_member",
              supplies_transport: true,
            },
          ],
        }),
    });
    return (
      <StoryProviders>
        <Catalog />
      </StoryProviders>
    );
  },
};

// The directory unreachable, which is the state every surface renders in for the
// first frame of a cold page. Every transport falls back to its raw id: a
// provider a human can read beats an empty cell, and nothing here waits on a
// fetch before it will draw.
export const DirectoryUnavailable: Story = {
  render: () => {
    installFetchStub({
      "GET /channel-providers": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <Catalog />
      </StoryProviders>
    );
  },
};
