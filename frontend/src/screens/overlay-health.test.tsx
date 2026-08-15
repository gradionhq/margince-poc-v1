/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import type { QueryLike } from "./common";
import {
  type Budget,
  OverlayLiveActions,
  OverlayLiveSection,
  type SyncStatus,
} from "./overlay-health";

// The live half of the overlay card takes its two reads as QueryLike props and
// its two grants as booleans, so it needs no network and no query client — it
// is exercised directly against hand-built fixtures rather than through the
// whole OverlayCard, the same seam overlay-health.stories.tsx uses. What is
// under test here is its honesty about WHY the action row is missing: this
// surface renders only on an installation already in overlay mode, so a seat
// without the grants is looking at a live mirror it cannot steer.
//
// The readings and the verbs are two components because the card hands them to
// two different slots of one Panel — the recessed plate and the action band —
// so they are rendered together here, exactly as OverlayCard composes them.

// A settled QueryLike. Written out rather than borrowed from a react-query
// result: this component only ever reads these five fields, and a hand-rolled
// UseQueryResult would assert far more shape than the seam promises.
function settled<T>(data: T): QueryLike<T> {
  return {
    isPending: false,
    isError: false,
    error: null,
    data,
    refetch: () => {},
  };
}

const SYNC: SyncStatus = {
  objects: [
    {
      object: "person",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
  ],
};

const BUDGET: Budget = {
  window: "2026-07-25T08:00:00Z/PT1H",
  consumed: 100,
  limit: 1000,
  band: "ok",
  headroom: "~unknown",
};

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

function renderSection(
  grants: Readonly<{
    canReconcile: boolean;
    canDisconnect: boolean;
    rolesKnown?: boolean;
  }>,
) {
  return render(
    <>
      <OverlayLiveSection
        sync={settled(SYNC)}
        budget={settled(BUDGET)}
        locale="en"
      />
      <OverlayLiveActions
        rolesKnown={grants.rolesKnown ?? true}
        canReconcile={grants.canReconcile}
        canDisconnect={grants.canDisconnect}
        onReconcile={() => {}}
        reconcilePending={false}
        reconcileQueued={false}
        reconcileError={null}
        onDisconnect={() => {}}
      />
    </>,
  );
}

afterEach(cleanup);

describe("the overlay card's live section", () => {
  it("states that the actions are withheld when neither grant is held", () => {
    renderSection({ canReconcile: false, canDisconnect: false });

    expect(screen.getByText(/do not have permission/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /sync now/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /disconnect/i })).toBeNull();
    // The health rows are a read, and a withheld write never withholds them:
    // the seat still sees how fresh the mirror is and what the budget is doing.
    expect(screen.getByText(/mirror sync/i)).toBeTruthy();
    expect(screen.getByText(/api budget/i)).toBeTruthy();
  });

  // Both grants read false while /me is in flight, and an unknown grant is not
  // a denial: saying so before the probe answers puts the sentence in front of
  // the very operator it does not apply to, on every load of the tab.
  it("says nothing about permission until the probe has answered", () => {
    renderSection({
      canReconcile: false,
      canDisconnect: false,
      rolesKnown: false,
    });

    expect(screen.queryByText(/do not have permission/i)).toBeNull();
    // And the reads it does have are still on screen — a probe still running is
    // no reason to withhold what already loaded.
    expect(screen.getByText(/mirror sync/i)).toBeTruthy();
  });

  // The other direction, three ways, because the two grants are independent
  // columns: an assertion only on the denied case would pass on a section that
  // shows the sentence beside the buttons it contradicts.
  it("offers the row and no notice to a seat holding both grants", () => {
    renderSection({ canReconcile: true, canDisconnect: true });

    expect(screen.getByRole("button", { name: /sync now/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /disconnect/i })).toBeTruthy();
    expect(screen.queryByText(/do not have permission/i)).toBeNull();
  });

  it("offers reconcile alone on the update grant, still without the notice", () => {
    renderSection({ canReconcile: true, canDisconnect: false });

    expect(screen.getByRole("button", { name: /sync now/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /disconnect/i })).toBeNull();
    // One held grant is not a read-only posture: the row exists, so a notice
    // claiming otherwise would deny an authority this seat plainly has.
    expect(screen.queryByText(/do not have permission/i)).toBeNull();
  });

  it("offers disconnect alone on the delete grant, still without the notice", () => {
    renderSection({ canReconcile: false, canDisconnect: true });

    expect(screen.getByRole("button", { name: /disconnect/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /sync now/i })).toBeNull();
    expect(screen.queryByText(/do not have permission/i)).toBeNull();
  });
});
