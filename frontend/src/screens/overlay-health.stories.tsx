// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { QueryLike } from "./common";
import {
  type Budget,
  OverlayLiveSection,
  type SyncStatus,
} from "./overlay-health";
import { StoryProviders } from "./story-utils";

// OverlayLiveSection stories for the fe-uat render gate: this file's own
// exported component, exercised directly against hand-built QueryLike
// fixtures (never a hand-rolled UseQueryResult — the same `common.tsx`
// QueryLike seam QueryGate/QueryStates already use) rather than through the
// full OverlayCard. overlay.stories.tsx already renders this component
// end-to-end off the real fetch stub; these stories cover it in isolation
// so overlay-health.tsx (a distinct changed file) carries its own story.

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
  title: "screens/overlay-health",
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
        canManage
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
// regardless (canManageOverlay in common.tsx).
export const ReadOnlySeat: Story = {
  render: () => (
    <StoryProviders>
      <OverlayLiveSection
        sync={query(syncFresh)}
        budget={query(budgetOk)}
        locale="en"
        canManage={false}
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
        canManage
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </StoryProviders>
  ),
};
