// The pipeline review: whats_slipping_this_week's ranked deals, worst first,
// each with the evidence its risk claim rests on.
//
// WHY THE EVIDENCE IS ON THE ROW AND NOT BEHIND A DISCLOSURE. The rank is a
// judgement, and the tool's whole contract is that a deal whose risk cannot be
// evidenced from its own fields is absent rather than guessed. A view that
// showed the ranking and hid the reasons would present that judgement as an
// oracle — which is the one reading the tool is written to prevent.
//
// It registers no tool of its own. `render_pipeline_review` is a document hung
// off a tool that already answers, which is what every `render_*` name on this
// surface is.

import { el, money, onResult } from "../bridge";
import { asList, asRecord, asText, type Warning } from "../types";
import "../view.css";

type SlippingDeal = {
  name: string;
  amount: string;
  evidence: { source: string; snippet: string }[];
};

/**
 * known narrows the untrusted payload, filtering BEFORE anything is counted so
 * the meta line describes what is actually shown.
 */
function known(data: Record<string, unknown>): SlippingDeal[] {
  return asList(data.deals)
    .filter(
      (d): d is Record<string, unknown> => typeof d === "object" && d !== null,
    )
    .map((d) => ({
      name: asText(d.name) || asText(d.deal_id),
      // Absent, not zero. A deal can be worked before it is priced, and a
      // blank amount rendered as a currency zero says it is worth nothing.
      amount: money(d.amount_minor, d.currency),
      evidence: asList(d.evidence).map(evidenceOf),
    }));
}

function evidenceOf(entry: unknown): { source: string; snippet: string } {
  const evidence = asRecord(entry);
  return { source: asText(evidence.source), snippet: asText(evidence.snippet) };
}

function dealRow(deal: SlippingDeal, position: number): HTMLElement {
  const row = el("div", "row");
  const head = el("div", "row-head");
  head.appendChild(el("span", "rank", `#${position}`));
  head.appendChild(el("span", "name", deal.name));
  head.appendChild(el("span", "score", deal.amount));
  row.appendChild(head);
  if (deal.evidence.length === 0) {
    // The tool does not answer an unevidenced deal, so this is a payload that
    // did not come from it. Saying so beats rendering a rank with no reason.
    row.appendChild(el("div", "state", "no evidence was sent for this deal"));
    return row;
  }
  for (const evidence of deal.evidence) {
    const line = el("div", "factors");
    line.appendChild(el("span", "factor", evidence.snippet));
    line.appendChild(el("span", "source", evidence.source));
    row.appendChild(line);
  }
  return row;
}

export function render(
  root: HTMLElement,
  data: unknown,
  _warnings: Warning[],
): void {
  root.replaceChildren();
  if (data === null || data === undefined) {
    root.appendChild(
      el("div", "empty", "The host sent no structured result for this review."),
    );
    return;
  }
  const deals = known(asRecord(data));
  root.appendChild(el("h1", undefined, "Pipeline review"));
  root.appendChild(
    el(
      "p",
      "meta",
      // "shown", not "at risk": the caller may have asked for a capped set,
      // and this document cannot tell a top-five from the whole answer. The
      // number describes the panel, which is a claim it can keep.
      `${deals.length} deal(s) shown, worst first — each with the field its risk was read off`,
    ),
  );
  if (deals.length === 0) {
    root.appendChild(
      el(
        "div",
        "empty",
        "No deal's risk can be evidenced from its own fields. " +
          "That is the answer, not a gap.",
      ),
    );
    return;
  }
  const rows = el("div", "rows");
  deals.forEach((deal, index) => {
    rows.appendChild(dealRow(deal, index + 1));
  });
  root.appendChild(rows);
}

onResult((data, warnings) => {
  const root = document.getElementById("root");
  // Guarded rather than asserted; see the account brief for why.
  if (root !== null) render(root, data, warnings);
});
