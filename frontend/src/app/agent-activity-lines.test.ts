import { describe, expect, it } from "vitest";
import { en, type MessageKey } from "../i18n/en";
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

  it("never says done about a run that stopped early", () => {
    for (const byState of Object.values(ACTIVITY_LINE)) {
      const degraded = copy(byState.degraded).toLowerCase();
      expect(degraded).not.toContain("done");
      expect(degraded).not.toContain("ready");
    }
  });

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

/** The catalog value behind a key the map is expected to carry. */
function copy(key: MessageKey | undefined): string {
  if (key === undefined) {
    throw new Error("the map names no key where one was expected");
  }
  return en[key];
}
