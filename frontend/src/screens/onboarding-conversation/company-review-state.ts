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
 * - `chosen`: the human settled it by picking one of the read's own
 *   candidates. It came off a page and keeps that page's quote when the read
 *   captured one, but nothing measured a confidence for it, so it is banded
 *   nowhere — an unmeasured value must not read as a weak or a strong one.
 * - `high` / `med` / `low`: site-grounded and measured, banded by the shared
 *   confidenceLevel thresholds, never a hand-written label beside a number.
 */
export type RowState =
  | "required"
  | "empty"
  | "typed"
  | "stored"
  | "chosen"
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
  chosen: 5,
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

// Why an empty field's collapsed row says what it says: the legal trio can
// name the gap from the crawl's own pages (see `legalFieldGap`); every other
// field falls back to the generic hint, since nothing in the read speaks to
// why they, specifically, are blank.
function emptyHintFor(
  field: CompanyFieldName,
  pages: CompanySiteRead["pages"] | undefined,
): MessageKey {
  const gap = legalFieldGap(field, pages);
  if (gap === "not-published") {
    return "ob.conv.triage.legalNotPublished";
  }
  if (gap === "not-checked") {
    return "ob.conv.triage.legalNotChecked";
  }
  return "ob.conv.triage.emptyHint";
}

export function rowFor(
  field: CompanyFieldName,
  draft: CompanyDraft,
  byName: ReadonlyMap<string, ProposalField>,
  t: ReturnType<typeof useT>,
  pages?: CompanySiteRead["pages"],
): ReviewRow {
  const value = draft.values[field];
  const base = {
    field,
    label: coldFieldLabel(field, t),
    value,
    multiline: isMultilineField(field),
  };
  if (value.trim() === "") {
    return {
      ...base,
      state: isRequired(field) ? "required" : "empty",
      evidence: null,
      confidence: null,
      emptyHintKey: emptyHintFor(field, pages),
    };
  }
  if (draft.edited.has(field)) {
    return {
      ...base,
      state: "typed",
      evidence: null,
      confidence: null,
      emptyHintKey: "ob.conv.triage.emptyHint",
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
      emptyHintKey: "ob.conv.triage.emptyHint",
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
      state: "chosen",
      evidence,
      confidence: null,
      emptyHintKey: "ob.conv.triage.emptyHint",
    };
  }
  return {
    ...base,
    state: confidenceLevel(confidence) ?? "low",
    evidence,
    confidence,
    emptyHintKey: "ob.conv.triage.emptyHint",
  };
}
