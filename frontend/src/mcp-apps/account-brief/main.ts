// The account brief view: read_brief's queue, with the factor decomposition
// each item ranked on.
//
// WHY THE FACTORS ARE THE POINT. The brief's own contract forbids the mystery
// number — an item that says only "this ranked first" restates the queue, while
// one that says "first on momentum and warmth" has told the person something.
// The score alone would fit in the chat text this view renders instead of; the
// five factors beside it are what a table buys.
//
// It renders and nothing else: no control, no action, no call back into the
// surface. Acting on a brief item is a human-only route by contract, so a button
// here would be a door the contract does not have.

import { count, el, onResult, percent } from "../bridge";
import { asList, asRecord, asText, type Warning } from "../types";
import "../view.css";

/** The factor keys the seam publishes, with the short label each shows under. */
const FACTORS: ReadonlyArray<readonly [string, string]> = [
  ["winnability", "win"],
  ["revenue", "rev"],
  ["timing", "time"],
  ["momentum", "mom"],
  ["warmth", "warm"],
];

/** One queue entry, after narrowing. Every member is what the view will draw,
 *  never what the host happened to send. */
type Item = {
  dealID: string;
  rank: unknown;
  composite: unknown;
  factors: Record<string, unknown>;
  state: string;
};

/**
 * queued narrows the untrusted payload to the rows this view will actually
 * draw, and it filters BEFORE anything is counted — a meta line describing rows
 * that were then skipped is a view claiming an answer it did not show.
 */
function queued(data: Record<string, unknown>): Item[] {
  return asList(data.items)
    .filter(
      (i): i is Record<string, unknown> => typeof i === "object" && i !== null,
    )
    .map((i) => ({
      dealID: asText(i.deal_id),
      rank: i.rank,
      composite: i.composite,
      factors: asRecord(i.factors),
      state: asText(i.state),
    }));
}

function factorRow(factors: Record<string, unknown>): HTMLElement {
  const wrap = el("div", "factors");
  for (const [key, label] of FACTORS) {
    wrap.appendChild(el("span", "factor", `${label} ${percent(factors[key])}`));
  }
  return wrap;
}

// The deal is named by ID because that is what the tool answers. A brief item
// carries no deal name, and inventing a lookup for one would be this view
// introducing a data path — which is exactly what an App must not do.
function itemRow(item: Item): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "rank", `#${count(item.rank)}`));
  head.appendChild(el("span", "name", item.dealID));
  head.appendChild(el("span", "score", percent(item.composite)));
  row.appendChild(head);
  row.appendChild(factorRow(item.factors));
  if (item.state !== "") {
    row.appendChild(el("div", "state", `state: ${item.state}`));
  }
  return row;
}

export function render(
  root: HTMLElement,
  data: unknown,
  _warnings: Warning[],
): void {
  // Replacing children rather than clearing markup: a view may be sent a second
  // result, and the first one's nodes have to go without any string ever being
  // parsed as markup.
  root.replaceChildren();
  if (data === null || data === undefined) {
    root.appendChild(
      el("div", "empty", "The host sent no structured result for this brief."),
    );
    return;
  }
  const answer = asRecord(data);
  const items = queued(answer);
  root.appendChild(el("h1", undefined, "Morning brief"));
  // candidate_count already reports what the ranking left out, which is this
  // view's whole completeness story — read_brief raises no truncation warning,
  // so there is no second condition to surface and no branch here for one.
  root.appendChild(
    el(
      "p",
      "meta",
      `${items.length} of ${count(answer.candidate_count)} candidates · as of ${
        asText(answer.as_of) || "unknown"
      }`,
    ),
  );
  if (items.length === 0) {
    root.appendChild(
      el(
        "div",
        "empty",
        "Nothing is queued. An empty brief is an answer, not a failure.",
      ),
    );
    return;
  }
  const rows = el("div", "rows");
  for (const item of items) rows.appendChild(itemRow(item));
  root.appendChild(rows);
}

onResult((data, warnings) => {
  const root = document.getElementById("root");
  // Guarded rather than asserted: the root is this document's own element, but
  // an assertion here would be the view promising something about a page it may
  // one day not be the only script on.
  if (root !== null) render(root, data, warnings);
});
