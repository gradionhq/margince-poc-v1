import { describe, expect, it } from "vitest";
import { de } from "../i18n/de";
import { en } from "../i18n/en";
import { vi } from "../i18n/vi";
import { ACTIVITY_LINE, displayedLines, lineFor } from "./ai-activity-lines";

// A key the map names must exist in the catalog, and a translated
// `agent.activity.` key nothing names is copy three translators paid for and
// no reader can reach. Both directions are reported as lists, because a wiring
// fix wants the whole set rather than the first thing that broke.
describe("the activity copy set", () => {
  it("is exactly the key set the map names", () => {
    const named = new Set<string>(
      displayedLines().flatMap(([, byState]) => Object.values(byState)),
    );
    const inCatalog = Object.keys(en).filter((key) =>
      key.startsWith("agent.activity."),
    );
    expect(
      [...named].filter((key) => !(key in en)),
      "keys the map names and no catalog carries",
    ).toEqual([]);
    expect(
      inCatalog.filter((key) => !named.has(key)),
      "copy translated three times and named by nothing",
    ).toEqual([]);
  });

  it("carries no placeholder — v1 lines are fixed literals", () => {
    for (const [key, value] of Object.entries(en)) {
      if (key.startsWith("agent.activity.")) {
        expect(value, key).not.toMatch(/\{/);
      }
    }
  });

  // The feature's most dangerous failure mode is telling someone a run
  // finished when it only got partway, so this is pinned in every locale it
  // ships in rather than English alone: a translator adding "fertig" to a
  // German degraded line would pass an English-only version of this test.
  it.each([
    { locale: "en", catalog: en, done: ["done"], ready: ["ready"] },
    { locale: "de", catalog: de, done: ["fertig"], ready: ["bereit"] },
    { locale: "vi", catalog: vi, done: ["xong"], ready: ["sẵn sàng"] },
  ])(
    "never says done or ready about a run that stopped early ($locale)",
    ({ catalog, done, ready }) => {
      for (const [, byState] of displayedLines()) {
        const key = byState.degraded;
        if (key === undefined) continue;
        const degraded = catalog[key as keyof typeof catalog].toLowerCase();
        for (const word of [...done, ...ready]) {
          expect(degraded).not.toContain(word);
        }
      }
    },
  );

  // Totality over (kind, state) is the COMPILER's, since ACTIVITY_LINE is typed
  // as a full Record — a hard-coded state list here could only check the states
  // somebody remembered to write down, and that list is what goes stale. What
  // is left for runtime is that each key resolves to real copy: a key that
  // typechecks but names no message renders as the key string to a reader.
  it("names copy that exists, for every displayed kind and state", () => {
    let checked = 0;
    for (const [kind, byState] of displayedLines()) {
      for (const [state, key] of Object.entries(byState)) {
        expect(
          en[key as keyof typeof en],
          `${kind}.${state} -> ${key}`,
        ).toBeTruthy();
        checked++;
      }
    }
    // A map that lost its entries would pass every assertion above.
    expect(checked).toBe(displayedLines().length * 6);
    // And a map that narrated NOTHING would pass that too.
    expect(displayedLines().length).toBeGreaterThan(0);
  });

  // A kind that is not narrated says why, in a sentence a reader of this file
  // can weigh. An empty reason is the same silence the rail's totality exists
  // to prevent, one level further in: it records that somebody chose, without
  // recording what they chose it for.
  it("gives every undisplayed kind a reason", () => {
    const undisplayed = Object.entries(ACTIVITY_LINE).filter(
      ([, entry]) => "notDisplayed" in entry,
    );
    expect(
      undisplayed.length,
      "no kind is undisplayed, so this proves nothing",
    ).toBeGreaterThan(0);
    for (const [kind, entry] of undisplayed) {
      const reason = "notDisplayed" in entry ? entry.notDisplayed : "";
      expect(reason.trim(), kind).not.toBe("");
    }
  });

  // The one state a reader must always be told about, whatever the work was.
  // It is the only state no writer produces — the server derives it — so a kind
  // that forgot it would go silent for exactly the case it exists to report.
  it("gives every displayed kind a line for the derived stalled state", () => {
    for (const [kind, byState] of displayedLines()) {
      expect(byState.stalled, kind).toBeDefined();
      expect(en[byState.stalled as keyof typeof en], kind).toBeTruthy();
    }
  });
});

describe("lineFor", () => {
  it("renders the line for a state that has copy", () => {
    expect(
      lineFor({ kind: "morning_brief", state: "running" }, (key) => en[key]),
    ).toBe(en["agent.activity.morningBrief.running"]);
  });

  // The map is total over the contract, so the only way to miss is a value the
  // contract does not carry — which is exactly what an OLDER TAB receives from
  // a newer server that has added a state or a kind. translate() falls back to
  // the key string, so without the existence check that reader is shown
  // `agent.activity.morningBrief.undefined` instead of nothing.
  //
  // The casts are the point rather than a shortcut: this asserts the runtime
  // behaviour for a value the type system has already ruled out, and there is
  // no other way to express it.
  it.each([
    ["an unknown state", { kind: "morning_brief", state: "hibernating" }],
    ["an unknown kind", { kind: "weekly_digest", state: "running" }],
    // The third way to draw nothing, and the one the server now produces by
    // the thousand: a kind this build reports and deliberately does not
    // narrate. It must read as silence, not as a message key.
    ["a kind the rail does not narrate", { kind: "summarize", state: "done" }],
  ])("renders nothing at all for %s", (_name, item) => {
    expect(lineFor(item, (key) => en[key])).toBeNull();
  });
});
