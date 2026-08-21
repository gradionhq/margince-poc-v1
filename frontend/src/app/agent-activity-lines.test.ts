import { describe, expect, it } from "vitest";
import { de } from "../i18n/de";
import { en } from "../i18n/en";
import { vi } from "../i18n/vi";
import { ACTIVITY_LINE, lineFor } from "./agent-activity-lines";

// A key the map names must exist in the catalog, and a translated
// `agent.activity.` key nothing names is copy three translators paid for and
// no reader can reach. Both directions are reported as lists, because a wiring
// fix wants the whole set rather than the first thing that broke.
describe("the activity copy set", () => {
  it("is exactly the key set the map names", () => {
    const named = new Set<string>(
      Object.values(ACTIVITY_LINE).flatMap((byState) => Object.values(byState)),
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
      for (const byState of Object.values(ACTIVITY_LINE)) {
        const key = byState.degraded;
        if (key === undefined) continue;
        const degraded = catalog[key].toLowerCase();
        for (const word of [...done, ...ready]) {
          expect(degraded).not.toContain(word);
        }
      }
    },
  );

  it("has a line for every state the runner can reach, and none for approval", () => {
    for (const byState of Object.values(ACTIVITY_LINE)) {
      for (const state of ["queued", "running", "done", "degraded", "failed"]) {
        expect(byState[state as keyof typeof byState], state).toBeDefined();
      }
      expect(byState.awaiting_approval).toBeUndefined();
    }
  });
});

describe("lineFor", () => {
  it("renders the line for a state that has copy", () => {
    expect(
      lineFor({ kind: "morning_brief", state: "running" }, (key) => en[key]),
    ).toBe(en["agent.activity.morningBrief.running"]);
  });

  // translate() falls back to the key string, so without the existence check a
  // reader would be shown `agent.activity.morningBrief.awaiting_approval`.
  it("renders nothing at all for a state with no copy", () => {
    expect(
      lineFor(
        { kind: "morning_brief", state: "awaiting_approval" },
        (key) => en[key],
      ),
    ).toBeNull();
  });
});
