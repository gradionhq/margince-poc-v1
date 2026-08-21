/** @vitest-environment jsdom */
import { cleanup, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { parseHash, routeHash } from "../app/router";
import { translate } from "../i18n";
import {
  ADMIN_SEGMENT,
  SETTINGS_TABS,
  SettingsScreen,
  type SettingsTabId,
  settingsAddress,
  settingsRouteTab,
} from "./settings";
import { render, renderNav, settingsBackend } from "./settings.testkit";

// WHERE a settings entry lives, and which addresses reach it. The admin half of
// settings sits one segment deeper than the personal half — `#/settings/privacy`
// became `#/settings/admin/privacy` — so three things have to hold together, and
// each of them is a different kind of claim:
//
//   - the ADDRESS a caller mints for an entry (settingsAddress),
//   - the ENTRY an address names, including the legacy shape every link written
//     before the segment existed still carries (settingsRouteTab),
//   - and the HREF a nav row actually carries, which is the one a reader clicks.
//
// The first two are derived from the SETTINGS_TABS register rather than restated
// beside it: a table of expected addresses is a second source of truth, and an
// entry that moves group would keep passing against it.

const LEGACY_ADMIN_ADDRESS = "#/settings/privacy";
const ADMIN_ADDRESS = "#/settings/admin/privacy";

beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
  globalThis.location.hash = "";
});

describe("settingsAddress — the address one entry lives at", () => {
  it("puts every admin entry under the admin segment, and leaves every personal one where it was", () => {
    // Swept over the register, so an entry that changes group changes what this
    // expects — which is the point: the depth is a property of the ENTRY, and a
    // caller that knew only the tab id could not work it out.
    for (const entry of SETTINGS_TABS) {
      expect(routeHash(settingsAddress(entry.id))).toBe(
        entry.group === "admin"
          ? `#/settings/${ADMIN_SEGMENT}/${entry.id}`
          : `#/settings/${entry.id}`,
      );
    }
  });

  it("spells the two shapes out", () => {
    // The sweep above is derived and would pass against a register that had gone
    // wrong in the same direction as the code. These two are the literal strings
    // a link in a channel carries.
    expect(routeHash(settingsAddress("privacy"))).toBe(ADMIN_ADDRESS);
    expect(routeHash(settingsAddress("voice"))).toBe("#/settings/voice");
  });

  it("keeps the shallow shape for an id no entry answers", () => {
    // A retired id is what a bookmark still carries — the audit trail was an
    // entry of its own before it moved onto Privacy & audit. It is not an admin
    // entry, so it gets no segment invented for it, and what it lands on is the
    // screen's fallback rather than this function's business.
    expect(routeHash(settingsAddress("audit"))).toBe("#/settings/audit");
  });

  it("addresses the section itself when no entry is named", () => {
    expect(routeHash(settingsAddress())).toBe("#/settings");
  });
});

describe("settingsRouteTab — which entry an address names", () => {
  it("resolves the address the product mints, and calls it current", () => {
    expect(settingsRouteTab(parseHash(ADMIN_ADDRESS))).toEqual({
      tab: "privacy",
      legacy: false,
    });
  });

  it("still resolves the legacy admin address, and flags it as legacy", () => {
    // A bookmark, a pasted link and a docs page must not land nowhere because the
    // IA grew a level of naming — and the flag is what stops the two spellings
    // both staying in circulation.
    expect(settingsRouteTab(parseHash(LEGACY_ADMIN_ADDRESS))).toEqual({
      tab: "privacy",
      legacy: true,
    });
  });

  it("resolves a personal address without flagging it", () => {
    // The personal entries never moved, so their own address is the current one
    // and nothing rewrites it.
    expect(settingsRouteTab(parseHash("#/settings/voice"))).toEqual({
      tab: "voice",
      legacy: false,
    });
  });

  it("resolves no entry for a personal one addressed through the admin segment", () => {
    // `#/settings/admin/voice` is not an address the product ever minted.
    // Answering it would give one page two live addresses, and the redirect above
    // would then have nothing to rewrite towards.
    expect(settingsRouteTab(parseHash("#/settings/admin/voice")).tab).toBe(
      undefined,
    );
  });

  it("resolves every address the register mints, and flags none of them", () => {
    // The round trip, over the whole register: mint the address, parse it back
    // the way the hash router does, and land on the entry it was minted for. A
    // group whose depth the two functions disagree about fails here rather than
    // as a blank page behind one row.
    for (const entry of SETTINGS_TABS) {
      expect(
        settingsRouteTab(parseHash(routeHash(settingsAddress(entry.id)))),
      ).toEqual({ tab: entry.id, legacy: false });
    }
  });
});

// The label a row carries back to the entry that published it, so the assertion
// below reads the register rather than a list of hrefs written out by hand.
const ENTRY_BY_LABEL = new Map<string, (typeof SETTINGS_TABS)[number]>(
  SETTINGS_TABS.map((entry) => [
    translate("en", `settings.tab.${entry.id satisfies SettingsTabId}`),
    entry,
  ]),
);

describe("the nav rows a reader actually clicks", () => {
  // The rail is the production wiring — the section this screen publishes,
  // rendered by the real level — so this is the href a reader gets, not the one
  // `settingsAddress` would have produced. The two are computed by different code
  // (the row goes through the entry's `prefix`), which is exactly why the row is
  // worth asserting separately.
  it("links each row at the depth its own group lives at", async () => {
    vi.stubGlobal("fetch", settingsBackend());
    renderNav();
    // The admin rows arrive with the /me answer, so waiting on one of them is
    // waiting for the level to be complete.
    await screen.findByRole("link", { name: "Data model" });
    const rows = screen
      .getAllByRole("link")
      .map((link) => ({
        label: link.textContent ?? "",
        href: link.getAttribute("href") ?? "",
      }))
      .filter((row) => row.href.startsWith("#/settings"));
    // A level that rendered nothing would pass every claim below it.
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      const entry = ENTRY_BY_LABEL.get(row.label);
      if (!entry) {
        throw new Error(`the level published a row for no entry: ${row.label}`);
      }
      expect(row.href).toBe(
        entry.group === "admin"
          ? `#/settings/${ADMIN_SEGMENT}/${entry.id}`
          : `#/settings/${entry.id}`,
      );
    }
  });
});

describe("a legacy admin address is answered and rewritten in place", () => {
  it("serves the page it names and moves the URL to the current address", async () => {
    vi.stubGlobal("fetch", settingsBackend());
    globalThis.location.hash = LEGACY_ADMIN_ADDRESS;
    // The address the reader came from is a real history entry, which is what
    // makes the count below mean anything.
    const entriesBefore = globalThis.history.length;

    render(<SettingsScreen route={parseHash(LEGACY_ADMIN_ADDRESS)} />);

    // They get the page they asked for — the rewrite is not a detour through
    // somewhere else.
    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    await waitFor(() => expect(globalThis.location.hash).toBe(ADMIN_ADDRESS));
    // REPLACED, not pushed: a pushed redirect leaves the address that redirects
    // sitting one step back, so Back lands on it, it redirects again, and the one
    // key a reader has for getting out of things cannot get them out.
    expect(globalThis.history.length).toBe(entriesBefore);
  });

  it("leaves a current address alone", async () => {
    // The mirror case, and the reason the flag exists rather than a rewrite on
    // every render: an address that is already the current one must not be
    // navigated at all.
    vi.stubGlobal("fetch", settingsBackend());
    globalThis.location.hash = ADMIN_ADDRESS;
    const entriesBefore = globalThis.history.length;

    render(<SettingsScreen route={parseHash(ADMIN_ADDRESS)} />);

    expect(
      await screen.findByRole("heading", { name: "Consent purposes" }),
    ).toBeTruthy();
    expect(globalThis.location.hash).toBe(ADMIN_ADDRESS);
    expect(globalThis.history.length).toBe(entriesBefore);
  });
});
