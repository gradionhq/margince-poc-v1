import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { de } from "../i18n/de";
import { en } from "../i18n/en";
import { vi } from "../i18n/vi";
import { NAMED, SAID, WROTE } from "./agentrail-copy";
import {
  ACTIVITY_LINE,
  displayedKinds,
  displayedLines,
  lineFor,
} from "./ai-activity-lines";

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
    // One case per REASON — all SIX. A single case would keep passing while the
    // others were narrated by accident, and the entries carrying a reason of
    // their own (`cert_judge`, `enrich`, and the site lanes) are exactly the
    // ones a "one example per shared constant" reading misses. A kind that
    // becomes displayed can no longer stand here, which is what this shape
    // catches.
    ["a background sweep", { kind: "brief_ranking", state: "done" }],
    ["work the asker is watching", { kind: "cold_start", state: "done" }],
    ["a task nothing has built", { kind: "nl_search", state: "done" }],
    ["the lane grading this build", { kind: "cert_judge", state: "done" }],
    // The two reasons carried by an entry of its own rather than a shared
    // constant, which is the pairing a hand-kept list drops first.
    ["a pass that reaches nobody", { kind: "enrich", state: "done" }],
    ["a site-read lane", { kind: "site_triage", state: "running" }],
  ])("renders nothing at all for %s", (_name, item) => {
    expect(lineFor(item, (key) => en[key])).toBeNull();
  });
});

// The site-read lanes carry ONE reason of their own, shared with nothing else.
//
// A prose reason cannot be checked by reading it, but the OBJECT a kind carries
// can be. These three sat on SYSTEM_SWEEP, whose sentence says the work belongs
// to nobody in particular — false whenever a human asked for the read, because
// compose binds that person as on_behalf_of and the occurrence lands in their
// own feed. A reader who trusted the shared sentence would conclude no site read
// can reach a person, and stop looking.
//
// Identity, not text: the reason may be reworded freely and only re-pointing a
// lane at another entry fails. Three assertions, because each catches a
// different way to be wrong and the obvious one-liner catches only the first:
//
//   same object   — the three cannot drift into three near-copies
//   notDisplayed  — a lane given a real LINE TABLE fails here. The version of
//                   this gate that compared reason STRINGS passed happily when
//                   two of the three were displayed, because the absent reason
//                   read as "" and "" is not the sweep's sentence. That is the
//                   inverse of the documented decision passing its own guard.
//   unshared      — "not the sweep's" is not "its own". Pointing all three at
//                   WATCHED_BY_THE_ASKER, or at any new shared sentence, is
//                   still the bug this exists to prevent.
describe("the site-read lanes carry one reason of their own", () => {
  const LANES = ["site_extract", "site_fact_extract", "site_triage"] as const;
  const entryFor = (kind: (typeof LANES)[number] | string) =>
    ACTIVITY_LINE[kind as (typeof LANES)[number]];

  it("is one entry object, not three near-copies", () => {
    expect(new Set(LANES.map(entryFor)).size).toBe(1);
  });

  it.each(LANES)("%s is undisplayed and says why", (kind) => {
    const entry = entryFor(kind);
    expect("notDisplayed" in entry).toBe(true);
    expect("notDisplayed" in entry ? entry.notDisplayed.trim() : "").not.toBe(
      "",
    );
  });

  it("shares that entry with no other kind", () => {
    const lanes = new Set<string>(LANES);
    const theirs = entryFor(LANES[0]);
    const trespassers = Object.entries(ACTIVITY_LINE)
      .filter(([kind, entry]) => !lanes.has(kind) && entry === theirs)
      .map(([kind]) => kind);
    expect(trespassers).toEqual([]);
  });
});

// The set the rail asks the server for, pinned.
//
// Not a tautology restating the map: it is the one place a kind ENTERS the
// product, and adding one has consequences a compiler cannot see. `enrich` was
// added here and reverted, because every occurrence of it is workspace-scoped —
// its only production site runs under a system principal with no on_behalf_of,
// and the personal feed selects on actor_user_id, so no reader could ever have
// been shown the copy that came with it.
//
// A failure here is not a bug. It means somebody widened what the rail draws,
// and owes two answers: can an occurrence of that kind reach ONE person's feed,
// and does it still fit inside `recent`'s cap of ten alongside the rest.
describe("the kinds the rail asks for", () => {
  it("is exactly the reviewed set", () => {
    expect([...displayedKinds()].sort()).toEqual([
      "document_extract",
      "draft_reply",
      "morning_brief",
      "offer_draft",
      "overnight_at_risk_sweep",
      "summarize",
    ]);
  });
});

// One action, one vocabulary.
//
// The taskbar ticker names work by react-query key; the rail names it by AI
// task. Where a key and a displayed kind denote THE SAME action, a reader meets
// two different sentences for one thing — the bar saying "Writing to Anna"
// while the panel says "I'm drafting your reply."
//
// The pairing is HAND-MAINTAINED and cannot be otherwise: nothing in the types
// connects a mutation key to the task it triggers, and that missing link is
// exactly what made `enrich` look visible when it was not — the ticker has an
// `enrich` key for work that never runs ai.TaskEnrich. So this list is a record
// of collisions somebody checked by reading both sides, and its value is that
// re-adding either half fails HERE rather than in front of a user.
describe("the ticker and the rail never narrate one action twice", () => {
  const COLLISIONS: readonly (readonly [
    tickerKey: string,
    railKind: string,
  ])[] = [
    // The DRAFT mutations carry ["email-draft", …] and are the draft_reply
    // call; the SEND mutations keep ["email", …] and are a plain write the rail
    // knows nothing about. They were ONE key until this pairing forced the
    // question, and deleting the shared entry silenced the sends — the split is
    // what lets each be narrated exactly once.
    ["email-draft", "draft_reply"],
  ];

  it.each(COLLISIONS)(
    "does not carry ticker key %s while the rail draws %s",
    (tickerKey, railKind) => {
      // A string compare over the drawn kinds, not `includes(x as never)`:
      // the cast would suppress a typo in COLLISIONS and quietly pass.
      const drawn = displayedKinds().some((kind) => kind === railKind);
      const narrated =
        Object.hasOwn(WROTE, tickerKey) ||
        Object.hasOwn(SAID, tickerKey) ||
        Object.hasOwn(NAMED, tickerKey);
      expect(drawn && narrated).toBe(false);
    },
  );
});

// The other half of the split, gated at the CALL SITES.
//
// The pairing above reads the ticker's tables, so it catches somebody re-adding
// a key there. It does not catch the same collision arriving from the other
// direction — a draft mutation renamed back to ["email", …] leaves both tables
// untouched and quietly restores two vocabularies for one action. That is the
// hole this closes, and finding it took mutating the call site and watching the
// first gate stay green.
//
// Read from source rather than imported: a mutationKey is a literal inside a
// hook, exported by nothing.
describe("the email mutation keys stay split", () => {
  const SITES = ["../screens/compose.tsx", "../screens/persondrawers.tsx"];

  it.each(SITES)("keeps drafts off the send key in %s", async (rel) => {
    const src = await readSource(rel);
    // One draft and one send per screen: the draft is the AI call the rail
    // narrates, the send is the write the ticker narrates.
    expect(count(src, '["email-draft",')).toBe(1);
    expect(count(src, '["email",')).toBe(1);
  });
});

function count(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

async function readSource(rel: string): Promise<string> {
  const here = dirname(fileURLToPath(import.meta.url));
  return readFile(join(here, rel), "utf8");
}
