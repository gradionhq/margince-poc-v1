/** @vitest-environment jsdom */
import { cleanup, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "./api/schema";
import {
  memoryStorage,
  renderApp,
  sessionOnlyFetch,
} from "./testing/appharness";

// A tab inside a record is a VIEW of that record, and the app keys its routed
// subtree on what the address is ABOUT (app/router.tsx's routeIdentity) so that
// moving between views is a re-render rather than a remount. What that buys is
// visible: the record's header, its readings and the tab strip the reader just
// clicked keep their DOM nodes, so design-system/enter.css plays the arrival
// animation for the panel that actually changed and for nothing else.
//
// It is asserted here rather than in screens/personpage.test.tsx because the key
// lives above every screen: that suite mounts the page with a `tab` prop and can
// never see the router decide.

type Person360 = components["schemas"]["Person360"];

const CAPTURED = {
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
} as const;

// The smallest record that draws a header and a tab strip. A panel showing an
// error is as good a panel as one showing data here, so nothing else is stubbed.
function person(id: string, name: string): Person360 {
  return {
    as_of: "2026-08-13T09:00:00Z",
    person: { id, full_name: name, ...CAPTURED },
    sections_omitted: [],
    activities: { data: [], page: { has_more: false } },
    deal_roles: { data: [], page: { has_more: false } },
    profile_fields: [],
  };
}

// The session harness's stub, with the one read the record page cannot render
// without answered on top of it. Everything else still 503s into its own error
// state — this suite is about which DOM nodes survive a navigation.
function personFetch() {
  const session = sessionOnlyFetch();
  return async (input: Request | string | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const match = /\/v1\/people\/(p-\d+)\/360$/.exec(url);
    if (match) {
      return new Response(
        JSON.stringify(person(match[1], `Person ${match[1]}`)),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    return session(input);
  };
}

// The one element every case below compares across a navigation: the RECORD's
// header, not the shell's top bar. It is the top of the page and the furthest
// thing from the panel that changed, so if it survives, everything between them
// did too.
function recordHead(): Element {
  const found = document.querySelector(".record-head");
  if (!found) {
    throw new Error("the record's header never rendered");
  }
  return found;
}

function tabStrip(): Element {
  const found = document.querySelector(".pe-tabs");
  if (!found) {
    throw new Error("the contact record's tab strip never rendered");
  }
  return found;
}

// Which tab the strip says is current. The tab arrives as a prop from the router,
// so this is the observable proof that the navigation reached the screen — and
// waiting on it is what makes the node comparisons below claims about a move
// that HAPPENED rather than one still in flight.
function currentTab(): string {
  const on = document.querySelector('.pe-tabs button[aria-pressed="true"]');
  if (!on) {
    throw new Error("no tab is current");
  }
  return on.textContent ?? "";
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  Object.defineProperty(globalThis.navigator, "languages", {
    value: ["fr-FR"],
    configurable: true,
  });
  vi.stubGlobal("fetch", vi.fn(personFetch()));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("the routed subtree's key", () => {
  it("keeps the record's chrome while a tab change swaps the panel", async () => {
    window.location.hash = "#/contacts/p-1/overview";
    renderApp();
    await waitFor(() => expect(currentTab()).toBe("Overview"));
    const chromeBefore = recordHead();
    const stripBefore = tabStrip();

    window.location.hash = "#/contacts/p-1/deals";
    await waitFor(() => expect(currentTab()).toBe("Deals"));

    // The same NODES, not merely equal markup: a remount would have replaced
    // both, and every block between them would have re-animated with them.
    expect(recordHead()).toBe(chromeBefore);
    expect(tabStrip()).toBe(stripBefore);
  });

  it("still throws the page away when the record itself changes", async () => {
    window.location.hash = "#/contacts/p-1/overview";
    renderApp();
    await waitFor(() => expect(currentTab()).toBe("Overview"));
    const chromeBefore = recordHead();

    // The control for the case above, and the reason the key exists at all: one
    // person's screen must never be reconciled into another's, or the state it
    // holds — an open drawer, a half-typed note — arrives on the wrong record.
    window.location.hash = "#/contacts/p-2/overview";
    await waitFor(() => expect(recordHead()).not.toBe(chromeBefore));
  });
});
