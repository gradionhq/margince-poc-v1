// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMemo } from "react";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import "./onboarding-payoff.css";

/**
 * The payoff at the end of onboarding: what those two minutes actually bought,
 * counted rather than congratulated.
 *
 * Every cell is a number the backend produced. The interesting half is the
 * distinction the type enforces: `null` is not `0`.
 *
 *  - a number, INCLUDING zero, is something the server told us. "0 people
 *    found" is a real answer about a site with no team page, and it is more
 *    useful than silence.
 *  - `null` is the absence of the operation that would have produced the
 *    number — no site read at all on the manual path, `pages_read` missing
 *    from the wire, a voice profile that was never built. A zero printed for
 *    an absent input reads as "you failed to do this", which is a lie about
 *    work that never ran.
 *
 * So an absent cell is never drawn as a 0. Where the absence itself is worth
 * stating it says so (`absentLabel`), and where it is not the cell is omitted:
 * a grid with nothing to report about pages is a grid without a pages cell,
 * not a placeholder that looks like data.
 */
export type PayoffCounts = Readonly<{
  /** Total facts the read produced (`CompanySiteRead.facts.length`). */
  factsRead: number | null;
  /** `OnboardingState.selected_fact_keys.length`. */
  factsConfirmed: number | null;
  /** People the read proposed (`CompanySiteRead.people.length`). */
  peopleFound: number | null;
  /** Confirmed profile fields. */
  profileFields: number | null;
  /** `CompanySiteRead.pages_read` — optional on the wire. */
  pagesRead: number | null;
  /** `VoiceCorpusSummary.total_words`; null when voice was skipped. */
  voiceWords: number | null;
}>;

type CountName = keyof PayoffCounts;

type CellSpec = Readonly<{
  name: CountName;
  label: MessageKey;
  /**
   * What the cell says when its number is absent. Only the voice corpus has
   * an answer: skipping voice training is a choice the flow offers, so "voice
   * not trained yet" is the honest reading of a missing word count. For every
   * other cell an absent number means the step behind it never produced one,
   * and there is nothing true to print in its place.
   */
  absentLabel?: MessageKey;
}>;

// Declaration order is render order. The array is the one place a cell exists;
// the `CountName` key ties each row to the prop it reads, so a renamed count
// fails to compile rather than silently rendering an empty cell.
const CELLS: readonly CellSpec[] = [
  { name: "factsRead", label: "ob.payoff.factsRead" },
  { name: "factsConfirmed", label: "ob.payoff.factsConfirmed" },
  { name: "peopleFound", label: "ob.payoff.peopleFound" },
  { name: "profileFields", label: "ob.payoff.profileFields" },
  { name: "pagesRead", label: "ob.payoff.pagesRead" },
  {
    name: "voiceWords",
    label: "ob.payoff.voiceWords",
    absentLabel: "ob.payoff.voiceNotTrained",
  },
];

export type PayoffGridProps = Readonly<{
  counts: PayoffCounts;
  /** BCP-47 tag for `Intl.NumberFormat` — grouping is a locale decision. */
  locale: string;
}>;

/**
 * The counts themselves: a description list, one hairline grid.
 *
 * `<dt>` precedes `<dd>` in the DOM because that is what a description list
 * is, and the cell reverses its own column so the number sits above its label
 * on screen. Assistive tech therefore reads "facts read, 218" — label first,
 * which is the order that makes a bare number mean something.
 */
export function PayoffGrid({ counts, locale }: PayoffGridProps) {
  const t = useT();
  // Grouping separators differ per locale ("1,284" vs "1.284"), and the
  // formatter is rebuilt only when the locale changes rather than per cell.
  const format = useMemo(() => new Intl.NumberFormat(locale), [locale]);

  return (
    <dl className="ob-payoff-grid">
      {CELLS.map((cell) => {
        const count = counts[cell.name];
        if (count === null) {
          return cell.absentLabel ? (
            <div className="ob-payoff-cell is-absent" key={cell.name}>
              {/* The label stays in the accessibility tree and leaves the
                  screen: "words in your voice — voice not trained yet" reads
                  correctly aloud, while printing both lines would stutter. */}
              <dt className="sr-only">{t(cell.label)}</dt>
              <dd className="ob-payoff-note">{t(cell.absentLabel)}</dd>
            </div>
          ) : null;
        }
        return (
          <div className="ob-payoff-cell" key={cell.name}>
            <dt className="ob-payoff-label">{t(cell.label)}</dt>
            <dd className="ob-payoff-value">{format.format(count)}</dd>
          </div>
        );
      })}
    </dl>
  );
}

export type PayoffMessageProps = Readonly<{
  counts: PayoffCounts;
  locale: string;
  onContinue: () => void;
}>;

/**
 * The grid plus the copy that frames it, and the one action that leaves it.
 *
 * The lead is a paragraph rather than a heading: the screen's `<h1>` belongs
 * to the step that owns it, and this sentence is prose however large it is set
 * (the same call `.auth-statement` makes).
 *
 * The two deferrals name their exits — "Settings → Autonomy", "Settings →
 * People" — inside their translated strings, so they are read as sentences and
 * never assembled from parts.
 */
export function PayoffMessage({
  counts,
  locale,
  onContinue,
}: PayoffMessageProps) {
  const t = useT();
  return (
    <section className="ob-payoff">
      <p className="ob-payoff-lead">{t("ob.payoff.lead")}</p>
      <PayoffGrid counts={counts} locale={locale} />
      <p className="ob-payoff-body">{t("ob.payoff.body")}</p>
      <ul className="ob-payoff-next">
        <li>{t("ob.payoff.defaults")}</li>
        <li>{t("ob.payoff.seats")}</li>
      </ul>
      <div className="ob-payoff-actions">
        <Button variant="primary" onClick={onContinue}>
          {t("ob.payoff.understood")}
        </Button>
      </div>
    </section>
  );
}
