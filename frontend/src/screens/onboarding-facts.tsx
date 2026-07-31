// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { components } from "../api/schema";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { confidenceLevel } from "./inbox";
import { MAX_SELECTED_FACTS } from "./onboarding";
import "./onboarding-facts.css";

// The fact selection surface: the preview card that sits in the review panel and
// the full table it opens.
//
// Both are PROP-DRIVEN. The chosen keys are persisted wizard state
// (`OnboardingState.selected_fact_keys`), so this module never fetches and never
// owns the list — it reads a `FactSelection` and reports changes upward. One
// selection model serves the card, the table and the payoff grid, because three
// copies of "is this fact saved?" is three chances for them to disagree.

type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];
type FactCategory = CompanySiteReadFact["category"];

// The preview shows the facts the reader is most likely to keep. Ten is what
// fits the panel without turning the card into the table it links to.
const PREVIEW_FACTS = 10;

// The closed wire enum `company|offering|market|signal`, spelled once as a
// label map keyed by the GENERATED union: a fifth category on the contract
// fails to compile here rather than quietly dropping out of the filter row and
// the proportion bar.
const CATEGORY_LABEL_KEY: Record<FactCategory, MessageKey> = {
  company: "ob.facts.catCompany",
  offering: "ob.facts.catOffering",
  market: "ob.facts.catMarket",
  signal: "ob.facts.catSignal",
};

function isFactCategory(value: string): value is FactCategory {
  return value in CATEGORY_LABEL_KEY;
}

// Chip and bar order is the label map's declaration order; the predicate keeps
// the keys typed without a cast.
const FACT_CATEGORIES: readonly FactCategory[] =
  Object.keys(CATEGORY_LABEL_KEY).filter(isFactCategory);

// `null` is the "All" chip: no category narrowing, not a fifth category.
type CategoryFilter = FactCategory | null;

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

function usePercentFormat(locale: string): Intl.NumberFormat {
  return useMemo(
    () =>
      new Intl.NumberFormat(locale, {
        style: "percent",
        maximumFractionDigits: 0,
      }),
    [locale],
  );
}

function categoryCounts(
  facts: readonly CompanySiteReadFact[],
): Map<FactCategory, number> {
  const counts = new Map<FactCategory, number>(
    FACT_CATEGORIES.map((category) => [category, 0]),
  );
  for (const fact of facts) {
    counts.set(fact.category, (counts.get(fact.category) ?? 0) + 1);
  }
  return counts;
}

function matching(
  facts: readonly CompanySiteReadFact[],
  category: CategoryFilter,
  query: string,
): CompanySiteReadFact[] {
  const needle = query.trim().toLowerCase();
  return facts.filter((fact) => {
    if (category !== null && fact.category !== category) {
      return false;
    }
    if (needle === "") {
      return true;
    }
    return (
      fact.value.toLowerCase().includes(needle) ||
      fact.evidence_snippet.toLowerCase().includes(needle)
    );
  });
}

// Highest confidence first, ties broken on the stable key so the preview does
// not reshuffle between renders of the same read.
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
 * read happened to emit last. Ordering here and the preview's ordering read the
 * same comparator, so the saved set and the shown set cannot disagree.
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

// The link text is the path, because two facts read off the same site differ
// only there and the host is already in the href. A root URL shows its host,
// and a value this cannot parse is shown verbatim rather than as an empty cell.
function sourceLabel(evidenceUrl: string): string {
  const match = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]+)(\/[^?#]*)?/i.exec(
    evidenceUrl,
  );
  if (match === null) {
    return evidenceUrl;
  }
  const path = (match[2] ?? "").replace(/\/$/, "");
  return path === "" ? match[1] : path;
}

function CategoryChips({
  facts,
  active,
  onPick,
  locale,
}: Readonly<{
  facts: readonly CompanySiteReadFact[];
  active: CategoryFilter;
  onPick: (next: CategoryFilter) => void;
  locale: string;
}>) {
  const t = useT();
  const counts = useCountFormat(locale);
  const byCategory = useMemo(() => categoryCounts(facts), [facts]);
  return (
    // A real fieldset, the way SegmentedControl does it, named for the numbers
    // it groups: each chip carries its own count as text, which is what lets the
    // proportion bar below stay aria-hidden.
    <fieldset className="ob-facts-chips" aria-label={t("ob.facts.mixLabel")}>
      <button
        type="button"
        className="ob-facts-chip"
        aria-pressed={active === null}
        onClick={() => onPick(null)}
      >
        {t("ob.facts.catAll")} <b>{counts.format(facts.length)}</b>
      </button>
      {FACT_CATEGORIES.map((category) => {
        const count = byCategory.get(category) ?? 0;
        return (
          <button
            key={category}
            type="button"
            className="ob-facts-chip"
            data-fact-category={category}
            aria-pressed={active === category}
            // A category the read found nothing for filters to an empty list;
            // the chip stays visible (the set is closed and worth seeing) but
            // does not offer a dead end.
            disabled={count === 0}
            onClick={() => onPick(category)}
          >
            <i className="ob-facts-dot" aria-hidden="true" />
            {t(CATEGORY_LABEL_KEY[category])} <b>{counts.format(count)}</b>
          </button>
        );
      })}
    </fieldset>
  );
}

/**
 * The category mix as one bar.
 *
 * `aria-hidden`, deliberately: every number it draws is already text on the
 * chips above it, so announcing the bar as well would read the same four counts
 * twice. The visual encoding is the only thing here a screen reader loses.
 */
function ProportionBar({
  facts,
}: Readonly<{ facts: readonly CompanySiteReadFact[] }>) {
  const byCategory = useMemo(() => categoryCounts(facts), [facts]);
  return (
    <div className="ob-facts-bar" aria-hidden="true">
      {FACT_CATEGORIES.map((category) => {
        const count = byCategory.get(category) ?? 0;
        if (count === 0) {
          return null;
        }
        return (
          <span
            key={category}
            data-fact-category={category}
            style={{ width: `${(count / facts.length) * 100}%` }}
          />
        );
      })}
    </div>
  );
}

function SelectionTally({
  facts,
  selection,
  locale,
}: Readonly<{
  facts: readonly CompanySiteReadFact[];
  selection: FactSelection;
  locale: string;
}>) {
  const t = useT();
  const counts = useCountFormat(locale);
  return (
    <div className="ob-facts-tally">
      <span className="ob-facts-count">
        {t("ob.facts.selected", {
          selected: counts.format(selection.selectedCount),
          total: counts.format(facts.length),
        })}
      </span>
      <span className="ob-facts-actions">
        <Button
          small
          // At the ceiling there is nothing left to add, and the notice beside
          // this says so.
          disabled={selection.allSelected || selection.atCap}
          onClick={() => selection.setAll(true)}
        >
          {t("ob.facts.selectAll")}
        </Button>
        <Button
          small
          disabled={selection.selectedCount === 0}
          onClick={() => selection.setAll(false)}
        >
          {t("ob.facts.clearAll")}
        </Button>
      </span>
    </div>
  );
}

/**
 * The ceiling, stated. The region is always in the DOM so assistive tech is
 * already watching it when the reader hits the cap; empty, it collapses.
 */
function CapNotice({
  atCap,
  locale,
}: Readonly<{ atCap: boolean; locale: string }>) {
  const t = useT();
  const counts = useCountFormat(locale);
  return (
    <p className="ob-facts-cap" role="status">
      {atCap
        ? t("ob.facts.capReached", { max: counts.format(MAX_SELECTED_FACTS) })
        : ""}
    </p>
  );
}

// The checkbox is genuinely disabled once the cap is reached rather than
// silently doing nothing when pressed; CapNotice carries the reason.
function saveDisabled(selection: FactSelection, selected: boolean): boolean {
  return !selected && selection.atCap;
}

function PreviewRow({
  fact,
  selection,
  locale,
}: Readonly<{
  fact: CompanySiteReadFact;
  selection: FactSelection;
  locale: string;
}>) {
  const t = useT();
  const percent = usePercentFormat(locale);
  const selected = selection.isSelected(fact);
  return (
    <li className="ob-facts-row" data-fact-category={fact.category}>
      <label className="ob-facts-pick">
        <input
          type="checkbox"
          checked={selected}
          disabled={saveDisabled(selection, selected)}
          aria-label={t("ob.facts.rowSave", { fact: fact.value })}
          onChange={() => selection.toggle(fact)}
        />
        <span className="ob-facts-body">
          <span className="ob-facts-val">{fact.value}</span>
          <span className="ob-facts-meta">
            <span className="ob-facts-cat">
              <i className="ob-facts-dot" aria-hidden="true" />
              {t(CATEGORY_LABEL_KEY[fact.category])}
            </span>
            <span className="ob-facts-conf">
              {percent.format(fact.confidence)}
            </span>
          </span>
          <q className="ob-facts-quote">{fact.evidence_snippet}</q>
        </span>
      </label>
    </li>
  );
}

/**
 * The preview card: the mix, the highest-confidence facts as checkbox rows, and
 * the way into the full table.
 *
 * Rows are keyed by `value_key`, so narrowing to a category and widening back
 * out re-uses the same row elements instead of rebuilding them around a
 * different fact.
 */
export function FactsCard({
  facts,
  selection,
  locale,
}: Readonly<{
  facts: readonly CompanySiteReadFact[];
  selection: FactSelection;
  locale: string;
}>) {
  const t = useT();
  const counts = useCountFormat(locale);
  const [category, setCategory] = useState<CategoryFilter>(null);
  const [tableOpen, setTableOpen] = useState(false);
  const preview = useMemo(
    () =>
      matching(facts, category, "").sort(byConfidence).slice(0, PREVIEW_FACTS),
    [facts, category],
  );

  if (facts.length === 0) {
    // The copy explains why the card is empty, which is the whole answer — a
    // spinner here would promise something this component cannot know.
    return (
      <section className="ob-facts">
        <h2 className="ob-facts-title">{t("ob.facts.title")}</h2>
        <p className="ob-facts-empty">{t("ob.facts.empty")}</p>
      </section>
    );
  }

  return (
    <section className="ob-facts">
      <div className="ob-facts-head">
        <h2 className="ob-facts-title">{t("ob.facts.title")}</h2>
        <SelectionTally facts={facts} selection={selection} locale={locale} />
      </div>
      <CategoryChips
        facts={facts}
        active={category}
        onPick={setCategory}
        locale={locale}
      />
      <ProportionBar facts={facts} />
      <CapNotice atCap={selection.atCap} locale={locale} />
      <ul className="ob-facts-preview">
        {preview.map((fact) => (
          <PreviewRow
            key={fact.value_key}
            fact={fact}
            selection={selection}
            locale={locale}
          />
        ))}
      </ul>
      <div className="ob-facts-foot">
        <p className="ob-facts-note">
          {t("ob.facts.previewNote", { count: counts.format(preview.length) })}
        </p>
        <Button variant="ghost" small onClick={() => setTableOpen(true)}>
          {t("ob.facts.openTable")}
        </Button>
      </div>
      {tableOpen && (
        <FactTable
          facts={facts}
          selection={selection}
          locale={locale}
          onClose={() => setTableOpen(false)}
        />
      )}
    </section>
  );
}

function FactTableRow({
  fact,
  selection,
  locale,
}: Readonly<{
  fact: CompanySiteReadFact;
  selection: FactSelection;
  locale: string;
}>) {
  const t = useT();
  const percent = usePercentFormat(locale);
  const selected = selection.isSelected(fact);
  return (
    <tr
      data-fact-category={fact.category}
      // The warn-family ground on a low-confidence row is where the reader's
      // judgment is actually needed; the percentage beside it carries the same
      // fact in text.
      data-confidence={confidenceLevel(fact.confidence) ?? undefined}
    >
      <td>
        <input
          type="checkbox"
          checked={selected}
          disabled={saveDisabled(selection, selected)}
          aria-label={t("ob.facts.rowSave", { fact: fact.value })}
          onChange={() => selection.toggle(fact)}
        />
      </td>
      <td>
        <span className="ob-facts-cat">
          <i className="ob-facts-dot" aria-hidden="true" />
          {t(CATEGORY_LABEL_KEY[fact.category])}
        </span>
      </td>
      <td className="ob-facts-td-fact">{fact.value}</td>
      <td className="ob-facts-td-src">
        <a href={fact.evidence_url} target="_blank" rel="noreferrer">
          {sourceLabel(fact.evidence_url)}
        </a>
        <q>{fact.evidence_snippet}</q>
      </td>
      <td className="ob-facts-td-conf">{percent.format(fact.confidence)}</td>
    </tr>
  );
}

/**
 * Every fact, searchable, in a real table.
 *
 * Portalled to `document.body` and that is load-bearing: the review panel this
 * opens from has a `backdrop-filter` and a transform, which make it the
 * containing block for any `position: fixed` descendant. Rendered in place, the
 * dialog covers its own column and nothing else.
 */
export function FactTable({
  facts,
  selection,
  onClose,
  locale,
}: Readonly<{
  facts: readonly CompanySiteReadFact[];
  selection: FactSelection;
  onClose: () => void;
  locale: string;
}>) {
  const t = useT();
  const counts = useCountFormat(locale);
  const [category, setCategory] = useState<CategoryFilter>(null);
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);
  const rows = useMemo(
    () => matching(facts, category, query),
    [facts, category, query],
  );

  // Focus moves to the search field on open and back to whatever opened the
  // dialog on unmount, so a keyboard reader is never dropped at the top of the
  // document with the panel gone.
  useEffect(() => {
    const opener = document.activeElement;
    search.current?.focus();
    return () => {
      if (opener instanceof HTMLElement) {
        opener.focus();
      }
    };
  }, []);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    globalThis.addEventListener("keydown", onKey);
    return () => globalThis.removeEventListener("keydown", onKey);
  }, [onClose]);

  // The dialog scrolls itself; the page behind it must not, or a wheel over the
  // scrim moves the wizard underneath.
  useEffect(() => {
    const { body } = document;
    const previous = body.style.overflow;
    body.style.overflow = "hidden";
    return () => {
      body.style.overflow = previous;
    };
  }, []);

  return createPortal(
    <div className="ob-facts-scrim">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={t("ob.facts.tableTitle")}
        className="ob-facts-modal"
      >
        <div className="ob-facts-modal-head">
          <h2>{t("ob.facts.tableTitle")}</h2>
          <div className="ob-facts-searchrow">
            <label className="ob-facts-search">
              <Search aria-hidden="true" />
              <input
                ref={search}
                type="search"
                value={query}
                aria-label={t("ob.facts.search")}
                placeholder={t("ob.facts.search")}
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
            <span className="ob-facts-hits">
              {t("ob.facts.hits", {
                hits: counts.format(rows.length),
                total: counts.format(facts.length),
              })}
            </span>
          </div>
          <CategoryChips
            facts={facts}
            active={category}
            onPick={setCategory}
            locale={locale}
          />
        </div>
        <div className="ob-facts-scroll">
          {rows.length === 0 ? (
            <p className="ob-facts-nomatch">{t("ob.facts.noMatch")}</p>
          ) : (
            <table className="ob-facts-table">
              <thead>
                <tr>
                  <th scope="col">{t("ob.facts.colSave")}</th>
                  <th scope="col">{t("ob.facts.colCategory")}</th>
                  <th scope="col">{t("ob.facts.colFact")}</th>
                  <th scope="col">{t("ob.facts.colSource")}</th>
                  <th scope="col" className="ob-facts-td-conf">
                    {t("ob.facts.colConfidence")}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((fact) => (
                  <FactTableRow
                    key={fact.value_key}
                    fact={fact}
                    selection={selection}
                    locale={locale}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
        <div className="ob-facts-modal-foot">
          <CapNotice atCap={selection.atCap} locale={locale} />
          <SelectionTally facts={facts} selection={selection} locale={locale} />
          <Button variant="primary" onClick={onClose}>
            {t("ob.facts.close")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
