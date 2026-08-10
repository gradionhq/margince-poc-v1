// The relationship map view: who_knows's colleagues, warmest first, with the
// interaction count the warmth rests on.
//
// WHY THE BAND AND THE COUNT TOGETHER. A strength score alone is the mystery
// number again, and the seam is careful about a specific case this view has to
// keep honest: strength is ABSENT when the band is "none", because never having
// spoken is not a score of zero. Rendering a missing strength as 0 would tell a
// rep a relationship decayed when none ever existed, so an absent score renders
// as absent.
//
// It renders and nothing else. Introducing someone is a human act with its own
// route; a button here would be this view inventing authority it was not given.

import { count, el, onResult, warned } from "../bridge";
import {
  asFiniteNumber,
  asList,
  asRecord,
  asText,
  type Warning,
} from "../types";
import "../view.css";

// The bands the seam reports. A band outside this set still renders — with no
// colour rather than with the wrong one — because the vocabulary belongs to the
// seam and a view that refused an unknown value would go blank the first time
// one was added.
const BANDS: Record<string, true> = {
  high: true,
  medium: true,
  low: true,
  none: true,
};

/** The envelope's code for "this read stopped at its bound". A bounded ranking
 *  is not the whole network, and the tool's contract is explicit that a model —
 *  or a view — told nothing will report it as one. */
const SWEEP_TRUNCATED = "sweep_truncated";

type Colleague = {
  name: string;
  bucket: string;
  strength: number | null;
  interactions: unknown;
};

/**
 * known narrows the untrusted payload, filtering BEFORE anything is counted:
 * the meta line and the empty state both have to describe what will actually be
 * shown. Counting the raw list and rendering the filtered one is how a view says
 * "3 colleagues" above no rows.
 */
function known(data: Record<string, unknown>): Colleague[] {
  return asList(data.colleagues)
    .filter(
      (c): c is Record<string, unknown> => typeof c === "object" && c !== null,
    )
    .map((c) => ({
      name: asText(c.display_name) || asText(c.user_id),
      bucket: asText(c.strength_bucket),
      strength: asFiniteNumber(c.strength),
      interactions: c.interactions_90d,
    }));
}

// Object.hasOwn, not a plain lookup: a bucket of "constructor" or "toString"
// finds a truthy value on the prototype chain and would be rendered as a class
// this stylesheet does not have.
function bandClass(bucket: string): string {
  return Object.hasOwn(BANDS, bucket) ? `band-${bucket}` : "state";
}

function strengthText(colleague: Colleague): string {
  const band = colleague.bucket || "unknown";
  // Absent, not zero. See the note at the top of this file.
  return colleague.strength === null ? band : `${band} · ${colleague.strength}`;
}

function colleagueRow(colleague: Colleague, position: number): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "rank", `#${position}`));
  head.appendChild(el("span", "name", colleague.name));
  head.appendChild(
    el("span", bandClass(colleague.bucket), strengthText(colleague)),
  );
  row.appendChild(head);
  row.appendChild(
    el(
      "div",
      "factors",
      `${count(colleague.interactions)} interactions in 90 days`,
    ),
  );
  return row;
}

/** The meta line. "warmest first" is only true of a COMPLETE ranking: when the
 *  read stopped at its bound these are the warmest FOUND, and saying otherwise
 *  is the claim the tool itself refuses to make. */
function metaLine(found: number, personID: string, bounded: boolean): string {
  if (bounded) {
    return `${found} colleague(s) found — more know this contact than are listed, so this is not the whole network`;
  }
  return `${found} colleague(s), warmest first · ${personID}`;
}

export function render(
  root: HTMLElement,
  data: unknown,
  warnings: Warning[],
): void {
  root.replaceChildren();
  if (data === null || data === undefined) {
    root.appendChild(
      el(
        "div",
        "empty",
        "The host sent no structured result for this contact.",
      ),
    );
    return;
  }
  const answer = asRecord(data);
  const colleagues = known(answer);
  root.appendChild(el("h1", undefined, "Who knows this contact"));
  root.appendChild(
    el(
      "p",
      "meta",
      metaLine(
        colleagues.length,
        asText(answer.person_id),
        warned(warnings, SWEEP_TRUNCATED),
      ),
    ),
  );
  if (colleagues.length === 0) {
    root.appendChild(
      el(
        "div",
        "empty",
        "Nobody here has spoken to this contact. That is the answer, not a gap.",
      ),
    );
    return;
  }
  const rows = el("div", "rows");
  colleagues.forEach((colleague, index) => {
    rows.appendChild(colleagueRow(colleague, index + 1));
  });
  root.appendChild(rows);
}

onResult((data, warnings) => {
  const root = document.getElementById("root");
  // Guarded rather than asserted; see the account brief for why.
  if (root !== null) render(root, data, warnings);
});
