// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { QueryLike } from "./common";
import {
  type Budget,
  OverlayLiveActions,
  OverlayLiveSection,
  type SyncStatus,
} from "./overlay-health";
import { StoryProviders } from "./story-utils";

// The overlay card's live half, for the fe-uat render gate: this file's own
// exported components, exercised directly against hand-built QueryLike
// fixtures (never a hand-rolled UseQueryResult — the same `common.tsx`
// QueryLike seam QueryGate/QueryStates already use) rather than through the
// full OverlayCard. overlay.stories.tsx already renders them end-to-end off
// the real fetch stub; these stories cover them in isolation so
// overlay-health.tsx (a distinct changed file) carries its own story.
//
// The readings and the verbs are two components because the card hands them to
// two different slots of one Panel — the recessed plate and the action band —
// so every story below renders the pair, exactly as OverlayCard composes them.

function query<T>(data: T | undefined): QueryLike<T> {
  return {
    isPending: false,
    isError: false,
    error: null,
    data,
    refetch: () => {},
  };
}

const syncFresh: SyncStatus = {
  objects: [
    {
      object: "person",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
    {
      object: "deal",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
  ],
};

const budgetOk: Budget = {
  window: "2026-07-25T08:00:00Z/PT1H",
  consumed: 100,
  limit: 1000,
  band: "ok",
  sources: { force_fresh: 20, poller: 70, capture: 10 },
  headroom: "~unknown",
  search: {
    window: "2026-07-25T08:00:00Z/PT1S",
    consumed: 2,
    limit: 20,
    band: "ok",
  },
};

const meta: Meta<typeof OverlayLiveSection> = {
  title: "Settings/Admin settings/Integrations/Overlay health",
  component: OverlayLiveSection,
};
export default meta;
type Story = StoryObj<typeof OverlayLiveSection>;

export const AdminWithActions: Story = {
  render: () => (
    <StoryProviders>
      <OverlayLiveSection
        sync={query(syncFresh)}
        budget={query(budgetOk)}
        locale="en"
      />
      <OverlayLiveActions
        rolesKnown
        canReconcile
        canDisconnect
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued={false}
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </StoryProviders>
  ),
};

// A rep/manager seat reads the same health rows but never sees the
// reconcile/disconnect controls — the server stays the RBAC authority
// regardless.
export const ReadOnlySeat: Story = {
  render: () => (
    <StoryProviders>
      <OverlayLiveSection
        sync={query(syncFresh)}
        budget={query(budgetOk)}
        locale="en"
      />
      <OverlayLiveActions
        rolesKnown
        canReconcile={false}
        canDisconnect={false}
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued={false}
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </StoryProviders>
  ),
};

export const ReconcileQueuedMessage: Story = {
  render: () => (
    <StoryProviders>
      <OverlayLiveSection
        sync={query(syncFresh)}
        budget={query(budgetOk)}
        locale="en"
      />
      <OverlayLiveActions
        rolesKnown
        canReconcile
        canDisconnect
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </StoryProviders>
  ),
};

// The same readings in dark, and the pairing to look at is the success Badge on
// the recessed plate.
//
// A toned Badge paints its tint over --bgElevated (atoms.css: .badge-success is
// --successBg composited on --bgElevated) while PanelPlate's ground is --bgCard,
// so the chip's fill is fixed by the THEME rather than by what it sits on — its
// legibility is therefore a property of the theme alone, and nothing but a
// pinned dark story asks the question. That exact chip-on-plate pairing measured
// 4.05:1 in light on this branch and had to be fixed; this is the dark half.
//
// Worth a look beside it: an untoned Badge is filled with --bgCard flat, the
// same token the plate under it uses — so a sync state or band this build does
// not recognise draws a chip with no fill of its own, in either theme.
export const AdminWithActionsDark: Story = {
  globals: { theme: "dark" },
  render: () => (
    <StoryProviders>
      <OverlayLiveSection
        sync={query(syncFresh)}
        budget={query(budgetOk)}
        locale="en"
      />
      <OverlayLiveActions
        rolesKnown
        canReconcile
        canDisconnect
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued={false}
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </StoryProviders>
  ),
};
