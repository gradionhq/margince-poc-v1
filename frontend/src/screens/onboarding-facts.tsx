// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMemo } from "react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import { confidenceLevel } from "./inbox";
import { MAX_SELECTED_FACTS } from "./onboarding";
import "./onboarding-facts.css";

// The fact selection model: which of a read's facts the reader has chosen to
// keep. Every surface that offers a fact toggle — the triage review's grouped
// list and the classic edit form's grid — reads the same `FactSelection`
// rather than keeping its own copy, so "is this fact saved?" cannot disagree
// between them.

type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];

/**
 * The one selection model. `isSelected`/`toggle` key off `fact.value_key`,
 * which is exactly what the server takes back in `selected_fact_keys`.
 */
export type FactSelection = {
  isSelected(fact: CompanySiteReadFact): boolean;
  toggle(fact: CompanySiteReadFact): void;
  setAll(on: boolean): void;
  selectedCount: number;
  allSelected: boolean;
  atCap: boolean;
};

/**
 * Wraps the persisted key list in the selection vocabulary the card and table
 * read.
 *
 * The cap is a contract limit (`selected_fact_keys` takes at most
 * MAX_SELECTED_FACTS), so it is enforced here rather than trusted to the
 * controls: `toggle` refuses to add past the ceiling and `setAll(true)` stops at
 * it. Callers surface `atCap` as `ob.facts.capReached` — a refusal that says why
 * beats a selection silently truncated on the way to the server.
 *
 * Additions append, so the persisted array keeps its order across a re-render
 * and a keys-only diff stays a keys-only diff. Keys already in the list that no
 * longer match a fact in `facts` are left alone: they belong to the wizard
 * state, not to this render.
 */
export function useFactSelection(
  facts: readonly CompanySiteReadFact[],
  selectedKeys: readonly string[],
  onChange: (keys: string[]) => void,
): FactSelection {
  return useMemo(() => {
    const chosen = new Set(selectedKeys);
    const atCap = chosen.size >= MAX_SELECTED_FACTS;
    return {
      isSelected: (fact) => chosen.has(fact.value_key),
      toggle: (fact) => {
        if (chosen.has(fact.value_key)) {
          onChange(selectedKeys.filter((key) => key !== fact.value_key));
          return;
        }
        if (atCap) {
          return;
        }
        onChange([...selectedKeys, fact.value_key]);
      },
      setAll: (on) => {
        if (!on) {
          onChange([]);
          return;
        }
        const next = [...selectedKeys];
        const have = new Set(next);
        for (const fact of facts) {
          if (next.length >= MAX_SELECTED_FACTS) {
            break;
          }
          if (have.has(fact.value_key)) {
            continue;
          }
          have.add(fact.value_key);
          next.push(fact.value_key);
        }
        onChange(next);
      },
      selectedCount: chosen.size,
      allSelected:
        facts.length > 0 && facts.every((fact) => chosen.has(fact.value_key)),
      atCap,
    };
  }, [facts, selectedKeys, onChange]);
}

function useCountFormat(locale: string): Intl.NumberFormat {
  return useMemo(() => new Intl.NumberFormat(locale), [locale]);
}

// Highest confidence first, ties broken on the stable key so the default
// selection does not reshuffle between renders of the same read.
function byConfidence(a: CompanySiteReadFact, b: CompanySiteReadFact): number {
  return (
    b.confidence - a.confidence || a.value_key.localeCompare(b.value_key, "en")
  );
}

/**
 * The keys a fresh read arrives with already ticked.
 *
 * A default selection is a JUDGEMENT, not a boast: a fact the shared confidence
 * scale calls low is exactly the one a person has to look at, so it arrives
 * unticked. What is left is taken most-certain-first, so when the contract
 * ceiling bites it drops the least certain fact rather than whichever ones the
 * read happened to emit last.
 */
export function defaultSelectedFactKeys(
  facts: readonly CompanySiteReadFact[],
): string[] {
  const keys = new Set<string>();
  for (const fact of [...facts].sort(byConfidence)) {
    if (keys.size >= MAX_SELECTED_FACTS) {
      break;
    }
    if (confidenceLevel(fact.confidence) === "low") {
      continue;
    }
    keys.add(fact.value_key);
  }
  return [...keys];
}

/**
 * The ceiling, stated. The region is always in the DOM so assistive tech is
 * already watching it when the reader hits the cap; empty, it collapses.
 *
 * The ceiling is ONE sentence, and more than one surface can be drawing it at
 * the same moment — the triage review's grouped facts section and the classic
 * edit form's fact grid can both be mounted at once. Two live regions flipping
 * on the same boundary read that sentence twice, so `live` says which notice
 * owns the announcement; the others show the same text silently.
 */
export function CapNotice({
  atCap,
  locale,
  live = true,
}: Readonly<{ atCap: boolean; locale: string; live?: boolean }>) {
  const t = useT();
  const counts = useCountFormat(locale);
  return (
    <p className="ob-facts-cap" role={live ? "status" : undefined}>
      {atCap
        ? t("ob.facts.capReached", { max: counts.format(MAX_SELECTED_FACTS) })
        : ""}
    </p>
  );
}

// The control is genuinely disabled once the cap is reached rather than
// silently doing nothing when pressed; CapNotice carries the reason. Every
// surface that offers a fact toggle asks this one question, so a checkbox in
// the triage review and a row in the edit form's grid refuse on the same
// terms.
export function saveDisabled(
  selection: FactSelection,
  selected: boolean,
): boolean {
  return !selected && selection.atCap;
}
