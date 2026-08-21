// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";
import { RECORD_ZONE } from "./timezone";

// A screen that names a zone has decided something, and the decision is the
// part that has to be reviewable: whose calendar does this date belong to, the
// organization's or the reader's? Spelled at the call site, that question gets
// answered once per screen by whoever is passing through, and the two answers
// drift apart — `timezone.ts` exists because the same product carried a fixed
// `Europe/Berlin` on a credential expiry (a personal deadline, wrong for every
// reader outside that zone) and a hand-inlined browser lookup on a record date
// (right for nobody, since two colleagues then quote different days for the
// same fact).
//
// So the rule is that a zone is NAMED in one file. `RECORD_ZONE` and
// `viewerZone()` carry the purposes; a call site picks one and reads as the
// judgement it is. This gate holds the negative half: nothing outside that
// module spells ANY zone out again — not the record zone, and not a second one
// chosen because it looked harmless.
//
// Every half is derived from the tree — each `.ts`/`.tsx` under `src/`, read
// through the parser, matched against the IANA zone shape and the two spellings
// of the browser lookup — so a screen added tomorrow is gated the day it lands,
// and so is a zone nobody has typed here yet. The one hand-maintained list is
// `pinnedZones` below, and every entry states why that file is allowed to name a
// zone. It cannot rot unnoticed: an entry that has stopped covering a real site
// fails.

const here = dirname(fileURLToPath(import.meta.url));
const srcRoot = join(here, "..");

// The one home for the rule. It names both zones because that is its job.
const zoneModule = join(here, "timezone.ts");

// The IANA areas every zone name starts with. Anchoring on them is what keeps
// `"screens/tasks"` and `"design-system/composed"` out of the findings while
// `"Asia/Ho_Chi_Minh"` stays in: a bare `Word/Word` shape would report half the
// module paths in the tree and be turned off within a week.
const IANA_AREAS =
  "Africa|America|Antarctica|Arctic|Asia|Atlantic|Australia|Etc|Europe|Indian|Pacific";

// A string literal that is ENTIRELY a zone name — the shape a call site passes
// to a formatter. ANY zone, not the record zone alone: `formatDate(x, locale,
// "UTC")` is the same judgement made silently, and so is a hand-picked
// `"America/Los_Angeles"`, so a gate that knew one name would wave the rest
// through. An i18n message that mentions a zone to explain the setting to a
// reader is prose inside a longer sentence, not a call site, and does not match.
const ZONE_LITERAL = new RegExp(
  `(['"\`])(?:UTC|(?:${IANA_AREAS})/[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)?)\\1`,
  "g",
);

// The viewer's own zone, read by hand. `viewerZone()` is the same lookup plus
// the UTC fallback for a runtime that reports no zone.
const INLINE_VIEWER_LOOKUP = /resolvedOptions\(\)\s*\.\s*timeZone/g;

// The lookup itself, whether or not `.timeZone` follows on the same line. This
// is what stops the rule being sidestepped by reading the options object into a
// local first, and it is the shape a test's zone spy is built from — which is
// why a suite that pretends to be elsewhere is an exemption below.
const RESOLVED_OPTIONS = /Intl\.DateTimeFormat\(\)\s*\.\s*resolvedOptions\(\)/g;

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" || entry.name === "dist"
        ? []
        : sourceFiles(path);
    }
    if (path === fileURLToPath(import.meta.url) || path === zoneModule) {
      // This gate spells out the very patterns it hunts for, and the module IS
      // the permitted site. A sweep that read either would vouch for itself.
      return [];
    }
    return /\.tsx?$/.test(entry.name) ? [path] : [];
  });
}

// A zone named in a COMMENT is not a call site. The invariant a screen states
// in prose — "pinned to Europe/Berlin it told a reader in another zone a
// different day" — is exactly what this rule wants written down, so a gate
// that punished it would delete its own explanation.
//
// Which spans ARE comments comes from the TypeScript parser rather than from a
// pattern over the text, because a comment opener is not something a pattern can
// settle. `/*` occurs inside ordinary string literals (`"/dist/mcp-apps/*.html"`)
// and inside line comments that quote a path (`// … /v1/public/*`); `//` occurs
// inside every URL; and a scan that pairs any of those with the next terminator
// blanks every line between the two, leaving the gate reading none of the real
// code it swallowed. Blindness in a gate is worse than a false finding: a
// finding gets looked at, and a blind spot reports success.
//
// Blanked rather than deleted, keeping the newlines: the findings below carry
// line numbers, and a comment that collapsed to one line would report every hit
// under it at the wrong place — a gate that names the wrong line sends the next
// reader to innocent code.
function code(path: string, source: string): string {
  const parsed = ts.createSourceFile(
    path,
    source,
    ts.ScriptTarget.Latest,
    // Parent pointers, so the walk below can ask a node for its child tokens.
    true,
    path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
  const chars = source.split("");
  const blank = ({ pos, end }: ts.CommentRange): void => {
    for (let index = pos; index < end; index++) {
      if (chars[index] !== "\n") {
        chars[index] = " ";
      }
    }
  };
  // Every comment is trivia of exactly one token, so walking to the tokens
  // reaches all of them, including any at the end of the file — those belong to
  // the end-of-file token. Leading AND trailing: the parser calls a comment that
  // shares a line with the code before it trailing, and reports it under that
  // name only.
  const strip = (node: ts.Node): void => {
    ts.getLeadingCommentRanges(source, node.pos)?.forEach(blank);
    ts.getTrailingCommentRanges(source, node.pos)?.forEach(blank);
    for (const child of node.getChildren(parsed)) {
      strip(child);
    }
  };
  strip(parsed);
  return chars.join("");
}

// Files that legitimately name a zone, each with the reason it does. A test or
// a fixture that PINS a zone is proving something about a named zone, which is
// the one thing `viewerZone()` cannot stand in for: a suite whose expected
// output moved with the machine it ran on would assert nothing.
const pinnedZones: { file: string; why: string }[] = [
  {
    file: "app/mefixture.ts",
    why: "The offline `me` fixture stands in for a real installation's stored settings, and its organization timezone is one of those settings — a value on the wire, not a zone this code picks.",
  },
  {
    file: "design-system/composed.stories.tsx",
    why: "TimelineRow takes its zone as a prop; the story has to hand it a named one to show what the row renders.",
  },
  {
    file: "design-system/composed.test.tsx",
    why: "Same prop, asserted: the row's rendered time is only checkable against a zone the test chose.",
  },
  {
    file: "design-system/explain.stories.tsx",
    why: "The FX-lineage panel takes `workspaceZone` as a prop and the story shows what a named one renders.",
  },
  {
    file: "design-system/explain.test.tsx",
    why: "Same prop, asserted against a fixed rate date.",
  },
  {
    file: "design-system/select.stories.tsx",
    why: "The zone picker's option list — IANA names as DATA the control lists, not a zone anything is formatted in.",
  },
  {
    file: "format/calendarday.test.ts",
    why: "middayInstant and calendarDay take a zone and are proven by naming two of them; a machine-dependent zone would make the expectations unwritable.",
  },
  {
    file: "format/format.test.ts",
    why: "formatDate's whole contract is that it attaches the zone it is given, which needs a named zone on both sides of the assertion.",
  },
  {
    file: "format/timezone.test.ts",
    why: "Pins viewerZone()'s answer and its UTC fallback by pretending to be in a named zone.",
  },
  {
    file: "screens/adddocument.test.tsx",
    why: "Installation-settings fixture: the organization timezone the document form reads back.",
  },
  {
    file: "screens/audit.test.tsx",
    why: "Pins an audit line to the organization's clock by reading it from a zone whose calendar day is already the next one — a claim that needs both the zone pretended in and the day compared against.",
  },
  {
    file: "screens/book.stories.tsx",
    why: "Renders the slot labels the booking screen shows, which needs a zone the story chose rather than the storybook host's.",
  },
  {
    file: "screens/company-context.test.tsx",
    why: "The signed-in `me` fixture carries the reader's own `timezone` — a settings value arriving on the wire, not a zone this screen picks.",
  },
  {
    file: "screens/companytasks.test.tsx",
    why: "Proves a task's deadline reads the same day on the row and in the detail modal, which only says anything from a zone where the record's clock has already turned over.",
  },
  {
    file: "screens/connected-agents.test.tsx",
    why: "Proves the record/viewer split on one row, which only works by naming both zones and a boundary-straddling instant.",
  },
  {
    file: "screens/consent.test.tsx",
    why: "Pins the proof log's rendered timestamps against a zone the test chose.",
  },
  {
    file: "screens/contractform.currency.test.tsx",
    why: "Installation-settings fixture: the organization timezone the contract form reads back.",
  },
  {
    file: "screens/dealbulk.stories.tsx",
    why: "Same `me` fixture behind the bulk-edit story: the reader's stored `timezone` is wire data the story has to supply.",
  },
  {
    file: "screens/deals-company.test.tsx",
    why: "Same `me` fixture behind the company-scoped board: a stored user setting, not a formatting decision.",
  },
  {
    file: "screens/deals-views.test.tsx",
    why: "Same `me` fixture behind the saved-views suite: a stored user setting, not a formatting decision.",
  },
  {
    file: "screens/deals.test.tsx",
    why: "Same `me` fixture across the board suite's five scenarios: a stored user setting, not a formatting decision.",
  },
  {
    file: "screens/inbox.test.tsx",
    why: "groupTask takes the zone that decides which day a task falls on, and the boundary cases are only writable against zones the test names — one at UTC, one west of it.",
  },
  {
    file: "screens/installation-settings.stories.tsx",
    why: "The Admin settings card DISPLAYS the configured organization timezone; the story needs a configured value.",
  },
  {
    file: "screens/installation-settings.test.tsx",
    why: "Asserts that same card shows the configured zone verbatim — the one screen where the zone name is the content.",
  },
  {
    file: "screens/integrations-provider.test.tsx",
    why: "The signed-in `me` fixture behind the provider screen carries the reader's stored `timezone` as wire data.",
  },
  {
    file: "screens/leads.test.tsx",
    why: "Installation-settings fixture backing the leads screen's settings read.",
  },
  {
    file: "screens/logactivity.test.tsx",
    why: "Reads back the calendar day an entry files under — backdated or logged at the moment — which needs both a named zone to pretend to be in and a named zone to compare the filing against.",
  },
  {
    file: "screens/oauthconsent.test.tsx",
    why: "The date-boundary case that started this rule: asserts the expiry does NOT read on a fixed zone's calendar, which needs that zone named to compare against.",
  },
  {
    file: "screens/personfiles.test.tsx",
    why: "Installation-settings fixture backing the person-files read.",
  },
  {
    file: "screens/privacy.logic.test.ts",
    why: "endOfDayInZone is a zone-taking function and is proven by naming one.",
  },
  {
    file: "screens/privacy.test.tsx",
    why: "Pretends to be in a zone behind UTC to prove a DSR deadline mints and reads back on the same calendar day; the claim is unwritable without naming that zone.",
  },
  {
    file: "screens/privacy.assignee.test.tsx",
    why: "Its `User` fixtures carry the member's own stored `timezone`, which is a field on the wire rather than a zone this code renders in.",
  },
  {
    file: "screens/quotas.forms.stories.tsx",
    why: "The `me` fixture behind the quota forms carries the reader's stored `timezone` as wire data.",
  },
  {
    file: "screens/quotas.stories.tsx",
    why: "Same `me` fixture behind the quotas story: a stored user setting the story has to supply.",
  },
  {
    file: "screens/quotas.test.tsx",
    why: "Same `me` fixture behind the quotas suite: a stored user setting, not a formatting decision.",
  },
  {
    file: "screens/scheduledsends.stories.tsx",
    why: "A scheduled send carries the zone the human picked it in; the story needs one that is NOT the host's, so it picks whichever of two named zones differs.",
  },
  {
    file: "screens/scheduledsends.test.tsx",
    why: "Same pair, asserted: the row must state the picked zone when it differs from the reader's.",
  },
  {
    file: "screens/tasks.stories.tsx",
    why: "A task's `scheduled_tz` is the zone the human picked it in — wire data the story supplies.",
  },
  {
    file: "screens/tasks.test.tsx",
    why: "Asserts a due time does NOT read on a fixed zone's calendar, which needs that zone named to compare against.",
  },
  {
    file: "screens/users-admin.stories.tsx",
    why: "Installation-settings fixtures backing the users-admin story rows.",
  },
];

type Finding = { file: string; line: number; text: string };

function findings(): Finding[] {
  return sourceFiles(srcRoot).flatMap((path) => {
    const rel = relative(srcRoot, path).split("\\").join("/");
    const lines = code(path, readFileSync(path, "utf8")).split("\n");
    return lines.flatMap((line, index) => {
      const hits = [
        ...line.matchAll(ZONE_LITERAL),
        ...line.matchAll(INLINE_VIEWER_LOOKUP),
        ...line.matchAll(RESOLVED_OPTIONS),
      ];
      return hits.map((hit) => ({
        file: rel,
        line: index + 1,
        text: hit[0],
      }));
    });
  });
}

const pinnedFiles = new Set(pinnedZones.map(({ file }) => file));

describe("a zone is named in one module", () => {
  const all = findings();

  it("reads the tree it is meant to sweep", () => {
    // A miswired walk passes every assertion below by inspecting nothing.
    expect(sourceFiles(srcRoot).length).toBeGreaterThan(200);
    expect(all.length).toBeGreaterThan(pinnedZones.length);
  });

  it("sees the record zone it exists to protect", () => {
    // The pattern is derived from the IANA shape, not copied from the value of
    // RECORD_ZONE — and the two must not drift, because the way that drift ends
    // is silent. Retune the record zone to something the pattern cannot see and
    // every arm below still passes, over a tree the sweep no longer reads.
    expect(`"${RECORD_ZONE}"`.match(ZONE_LITERAL)).toEqual([
      `"${RECORD_ZONE}"`,
    ]);
    // And it has to be a zone Intl accepts, or the shape matched a name that
    // formats nothing.
    expect(
      () => new Intl.DateTimeFormat("en", { timeZone: RECORD_ZONE }),
    ).not.toThrow();
  });

  it("sees a zone that is not the record zone", () => {
    // The gate's first spelling knew one name, so `formatDate(x, locale, "UTC")`
    // and a hand-picked `"America/Los_Angeles"` passed it untouched: two zone
    // decisions taken at a call site, neither reviewable. Both are the finding.
    const zones = ['"UTC"', "'America/Los_Angeles'", '"Asia/Ho_Chi_Minh"'];
    for (const zone of zones) {
      expect(zone.match(ZONE_LITERAL)).toEqual([zone]);
    }
    // A module path is the shape's near neighbour and is not a zone. Reporting
    // these is how a gate gets switched off rather than fixed.
    for (const path of [
      '"screens/tasks"',
      '"../format/timezone"',
      '"/v1/me"',
    ]) {
      expect(path.match(ZONE_LITERAL)).toBeNull();
    }
  });

  it("reads the code a comment sits beside, and the code beneath one", () => {
    // A comment opener is not a lexical fact a pattern can settle: `/*` sits
    // inside path strings and inside line comments that quote a path, and `//`
    // sits inside every URL. Pair one of those with the next terminator and the
    // real code between them stops being swept — which is a gate reporting
    // success over lines it never read.
    const source = [
      'const glob = "/dist/mcp-apps/*.html";',
      "// the api client carves out /v1/public/* for this",
      'const hidden = "Europe/Berlin";',
      "/** A doc comment naming Europe/Berlin in prose. */",
      'const url = "https://example.test/x"; // trailing Europe/Berlin prose',
      'const shown = "Asia/Ho_Chi_Minh";',
    ].join("\n");
    const swept = code("sample.ts", source)
      .split("\n")
      .flatMap((line, index) =>
        [...line.matchAll(ZONE_LITERAL)].map(
          (hit) => `${index + 1}: ${hit[0]}`,
        ),
      );
    expect(swept).toEqual([`3: "Europe/Berlin"`, `6: "Asia/Ho_Chi_Minh"`]);
  });

  it("leaves no screen naming a zone of its own", () => {
    const loose = all
      .filter(({ file }) => !pinnedFiles.has(file))
      .map(({ file, line, text }) => `${file}:${line}: ${text}`);
    // Zero, not a ratchet. A date on a record surface takes RECORD_ZONE; a
    // moment the reader relates to their own clock takes viewerZone(); a
    // date-only wire value takes RECORD_ZONE, because there is no instant in
    // `2026-08-21` to localize and a zone behind UTC prints the day before.
    // A fixture or a test that must pin a zone earns an entry in
    // `pinnedZones` with the reason it needs one.
    expect(loose.sort()).toEqual([]);
  });

  it("keeps no exemption that has stopped covering a site", () => {
    // An allowlist is only as honest as its narrowest entry. Once the last
    // pinned zone in a file is gone, the entry is a standing permission
    // nobody needs — and the next screen that hard-codes a zone in that file
    // inherits the excuse.
    const covered = new Set(all.map(({ file }) => file));
    const idle = pinnedZones
      .filter(({ file }) => !covered.has(file))
      .map(({ file }) => file);
    expect(idle.sort()).toEqual([]);
  });

  it("states a reason for every exemption", () => {
    const unexplained = pinnedZones
      .filter(({ why }) => why.trim().length < 40)
      .map(({ file }) => file);
    // A reasonless entry is itself the finding: it records that somebody
    // wanted the gate quiet, not that the file has a case.
    expect(unexplained).toEqual([]);
  });
});
