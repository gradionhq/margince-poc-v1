// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../../api/schema";
import type { Evidence } from "../../design-system/trust";
import type { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import { coldFieldLabel } from "../common";
import { confidenceLevel } from "../inbox";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import {
  CUSTOMER_FIELDS,
  isMultilineField,
  isRequired,
  LEGAL_IDENTITY_FIELDS,
  OFFER_FIELDS,
  provenanceOf,
  SALES_FIELDS,
} from "../onboarding";
import type { LegalFieldGap } from "./company-proposal";
import { legalFieldGap } from "./company-proposal";

// Where a field on the review board stands, and the ONE place that derives
// it. Four surfaces read this and must never disagree: the review board's
// own section nav and row marks (confirm-card.tsx), the rail's to-do list,
// and the narration that counts open decisions — all of them ask `isWork`
// and `rowFor` here rather than keeping their own idea of "outstanding".
// Change the rule once, in this file, and every surface ticks over together.
//
// No module-level code below touches `../onboarding`'s exports directly —
// `provenanceOf`/`isMultilineField`/`isRequired` are only CALLED, inside
// `rowFor`'s body, and the field-group consts only inside `reviewFields`'s,
// never read at the top of this file. confirm-card.tsx sits on an import
// cycle with `../onboarding` (its own comment on `reviewGroups()` explains
// why), because it keeps a module-level table built FROM `../onboarding`'s
// exports; this module keeps no such table, so it does not reproduce that
// crash risk even though it still participates in the same cycle by
// importing `../onboarding` at all.

type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

/**
 * Where a field stands, derived from the draft and the proposal in one place
 * so the tallies, the section nav and the row markers cannot disagree:
 * - `required` / `empty`: no value; required is the blocking kind.
 * - `typed`: the human wrote it this session: human truth, no meter.
 * - `stored`: carried in from the existing profile untouched (member path).
 * - `quoted`: the value came off the site's own legal notice, through the
 *   entity census rather than the graded extraction lane — either as the one
 *   company the site names, or as the candidate the human picked from
 *   several. It keeps that page's quote when the read captured one, but
 *   nothing measured a confidence for it, so it is banded nowhere: an
 *   unmeasured value must not read as a weak or a strong one.
 * - `high` / `med` / `low`: site-grounded and measured, banded by the shared
 *   confidenceLevel thresholds, never a hand-written label beside a number.
 */
export type RowState =
  | "required"
  | "empty"
  | "typed"
  | "stored"
  | "quoted"
  | "high"
  | "med"
  | "low";

export type ReviewRow = {
  field: CompanyFieldName;
  label: string;
  value: string;
  multiline: boolean;
  state: RowState;
  evidence: Evidence | null;
  /** The raw score behind a banded state; null when nothing was graded. */
  confidence: number | null;
  /** The collapsed row's empty-state copy. Generic for most fields; the
   * legal trio names WHY it's empty when the crawl can say — see
   * `legalFieldGap`. Only meaningful when `value` is blank. */
  emptyHintKey: MessageKey;
  /** Why the read came back without this field, when the read itself accounts
   * for it, and null when nothing does. The board states an omission only on
   * this: it is a claim about what the crawl DID, and the only per-field
   * evidence on the wire is the legal trio's own pages and candidates. For
   * every other field the wire carries silence, and silence has causes this
   * surface cannot tell apart — a crawl that stopped early, an extraction that
   * abstained, a value the read proposed and the human then cleared. Only
   * meaningful when `value` is blank. */
  omissionReasonKey: MessageKey | null;
};

/** Lower sorts first: the work goes to the top, the settled to the bottom. */
export const STATE_RANK: Readonly<Record<RowState, number>> = {
  required: 0,
  low: 1,
  empty: 2,
  med: 3,
  high: 4,
  typed: 5,
  stored: 5,
  quoted: 5,
};

/** A row that still wants a decision or a value, as opposed to a skim row. */
export function isWork(state: RowState): boolean {
  return STATE_RANK[state] < STATE_RANK.high;
}

// The four field groups in one fixed order, flattened: confirm-card.tsx's
// board and the rail's to-do list both walk this exact list through
// `rowFor` and `isWork`, so a field can never turn up outstanding on one
// surface and absent from the other.
export function reviewFields(): readonly CompanyFieldName[] {
  return [
    ...LEGAL_IDENTITY_FIELDS,
    ...OFFER_FIELDS,
    ...CUSTOMER_FIELDS,
    ...SALES_FIELDS,
  ];
}

// What a row with no value says for itself, when the read can say anything
// about it at all: the legal trio's gap, named from the crawl's own pages
// (see `legalFieldGap`). Null for every other field, and for every field on
// the manual path — the read speaks to why THOSE are blank nowhere, and a
// sentence about a crawl is not something to fall back on.
const GAP_REASON: Readonly<Record<LegalFieldGap, MessageKey>> = {
  unpicked: "ob.conv.triage.legalUnpicked",
  "not-published": "ob.conv.triage.legalNotPublished",
  "not-checked": "ob.conv.triage.legalNotChecked",
};

// The placeholder an empty row shows where its value would be. It stands in
// for a value, so it claims nothing about what was looked for: a field with a
// read-derived gap prints that gap, and every other one prints the plain
// "still empty" line, whether or not a read ever ran.
const GENERIC_EMPTY_HINT: MessageKey = "ob.conv.triage.emptyHint";

function gapReasonFor(
  field: CompanyFieldName,
  draft: CompanyDraft,
  pages: CompanySiteRead["pages"] | undefined,
  entities: CompanySiteRead["legal_entities"] | undefined,
): MessageKey | null {
  const gap = legalFieldGap(field, pages, draft, entities);
  return gap === null ? null : GAP_REASON[gap];
}

export function rowFor(
  field: CompanyFieldName,
  draft: CompanyDraft,
  byName: ReadonlyMap<string, ProposalField>,
  t: ReturnType<typeof useT>,
  pages?: CompanySiteRead["pages"],
  entities?: CompanySiteRead["legal_entities"],
): ReviewRow {
  const value = draft.values[field];
  // The empty-state pair carried by every branch, so a row that HAS a value
  // never leaves either one half-set: it has no empty state to describe and
  // nothing to call omitted.
  const base = {
    field,
    label: coldFieldLabel(field, t),
    value,
    multiline: isMultilineField(field),
    emptyHintKey: GENERIC_EMPTY_HINT,
    omissionReasonKey: null,
  };
  if (value.trim() === "") {
    const reason = gapReasonFor(field, draft, pages, entities);
    return {
      ...base,
      state: isRequired(field) ? "required" : "empty",
      evidence: null,
      confidence: null,
      emptyHintKey: reason ?? GENERIC_EMPTY_HINT,
      omissionReasonKey: reason,
    };
  }
  if (draft.edited.has(field)) {
    return {
      ...base,
      state: "typed",
      evidence: null,
      confidence: null,
    };
  }
  // Grounding precedence: the draft's CURRENT provenance (an entity pick
  // re-grounds the legal block), then the proposal's own evidence — but the
  // proposal's evidence supports the value IT proposed, never whatever value
  // happens to be sitting in the draft. A row still showing the existing
  // profile value (the proposal disagreed, or nobody has resolved which one
  // wins yet) must not borrow the new claim's confidence and snippet as if
  // they backed the old value.
  //
  // ONE of the two answers for the whole row, never a blend: a quote from the
  // draft's own grounding beside a score from the proposal would describe the
  // same value with two provenances at once.
  const proposed = byName.get(field);
  // Trimmed on both sides: a stored profile value can carry surrounding
  // whitespace (formFromProfile copies profile strings untouched), and a
  // strict compare against the raw draft value would drop the proposal's own
  // evidence over nothing more than that whitespace — the same value, read
  // as if the proposal never backed it.
  const provenance =
    provenanceOf(draft, field) ??
    (proposed !== undefined && proposed.value.trim() === value.trim()
      ? proposed
      : null);
  if (provenance === null) {
    return {
      ...base,
      state: "stored",
      evidence: null,
      confidence: null,
    };
  }
  const snippet = provenance.evidence_snippet;
  // Evidence-or-omit: no verbatim quote, no evidence line — never an empty
  // one, and never the value standing in as its own proof.
  const evidence =
    snippet === undefined || snippet.trim() === ""
      ? null
      : { snippet, source: provenance.source_url ?? "" };
  const { confidence } = provenance;
  if (confidence === undefined) {
    return {
      ...base,
      state: "quoted",
      evidence,
      confidence: null,
    };
  }
  return {
    ...base,
    state: confidenceLevel(confidence) ?? "low",
    evidence,
    confidence,
  };
}
