// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Check, ChevronDown, Circle } from "lucide-react";
import { type ReactNode, useState } from "react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { namedSiteReadKind } from "./common";
import "./onboarding-live-panel.css";

// The coverage card: what a company site read covered and what it could not,
// folded into the triage review's grouped surface. Purely presentational —
// every value arrives as a prop, so it renders identically from a live read
// and a test.

type SitePage = components["schemas"]["CompanySiteReadPage"];

// Shape, not only colour, separates a settled card from one still shut: a
// filled check versus an open ring.
function CardGlyph({ done }: Readonly<{ done: boolean }>) {
  if (done) {
    return <Check className="ob-live-card-glyph" aria-hidden size={14} />;
  }
  return <Circle className="ob-live-card-glyph" aria-hidden size={14} />;
}

/**
 * A folding card in the dossier stack. Collapsed by default — the count in
 * its header is what makes that safe.
 */
export function DossierCard({
  title,
  count,
  done,
  children,
}: Readonly<{
  title: string;
  count?: string;
  done?: boolean;
  children: ReactNode;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <section className="ob-live-card" data-done={done === true}>
      <button
        type="button"
        className="ob-live-card-head"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <CardGlyph done={done === true} />
        <span className="ob-live-card-title">{title}</span>
        {count !== undefined && (
          <span className="ob-live-card-count">{count}</span>
        )}
        <span className="ob-live-card-toggle">
          {t(open ? "ob.live.hide" : "ob.live.review")}
        </span>
        <ChevronDown className="ob-live-card-chev" aria-hidden size={14} />
      </button>
      {open && <div className="ob-live-card-body">{children}</div>}
    </section>
  );
}

/**
 * What kind of gap a coverage row is. A page the crawler chose not to fetch is
 * routine housekeeping, a page it could not fetch is a hole in the read, and a
 * warning is a caveat about the read as a whole — three different things a
 * reader must be able to tell apart at a glance.
 */
type CoverageKind = "warn" | "skip" | "fail";

type CoverageRow = Readonly<{
  id: string;
  kind: CoverageKind;
  label: string;
  /**
   * Which page this was, when the read named one worth naming. Absent for a
   * warning (no page) and for a kind that says nothing the label does not.
   */
  name?: string;
  url?: string;
  reason: string;
}>;

// Every gap the read admits to, derived from the read itself: the warnings it
// raised, the pages robots.txt or a fetch error kept it out of. There is no
// hand-kept list here, so a new skip reason surfaces the moment the wire
// carries it.
function coverageRows(
  pages: readonly SitePage[],
  warnings: readonly string[],
  t: ReturnType<typeof useT>,
): CoverageRow[] {
  const rows: CoverageRow[] = [];
  let seq = 0;
  for (const warning of warnings) {
    seq += 1;
    rows.push({
      id: `warning:${seq}`,
      kind: "warn",
      label: t("ob.live.coverageWarning"),
      reason: warning,
    });
  }
  const groups: ReadonlyArray<{
    status: SitePage["status"];
    kind: CoverageKind;
    key: MessageKey;
  }> = [
    { status: "skipped", kind: "skip", key: "ob.live.coverageSkipped" },
    { status: "failed", kind: "fail", key: "ob.live.coverageFailed" },
  ];
  for (const { status, kind, key } of groups) {
    for (const page of pages.filter((page) => page.status === status)) {
      seq += 1;
      // The page's own kind is the reader's answer to "what did you miss?"; the
      // URL alone makes them decode a path. When the wire names no kind, or
      // names "other", the status label is already everything there is to say.
      const named = namedSiteReadKind(page.kind);
      rows.push({
        id: `${status}:${seq}`,
        kind,
        label: t(key),
        name: named === undefined ? undefined : t(named),
        url: page.url,
        reason: page.reason ?? t("ob.scan.pageNoReason"),
      });
    }
  }
  return rows;
}

/**
 * What the read covered and what it could not. Its count is the read/skipped
 * split, derived by filtering the pages the wire returned — there is no
 * page-count denominator to show a ratio against.
 */
export function CoverageCard({
  pages,
  warnings,
}: Readonly<{ pages: readonly SitePage[]; warnings: readonly string[] }>) {
  const t = useT();
  const rows = coverageRows(pages, warnings, t);
  const read = pages.filter((page) => page.status === "fetched").length;
  const skipped = pages.filter((page) => page.status === "skipped").length;
  return (
    <DossierCard
      title={t("ob.live.cardCoverage")}
      count={t("ob.live.countPages", { read, skipped })}
      done={rows.length === 0}
    >
      {rows.length === 0 ? (
        <p className="ob-live-empty">{t("ob.live.coverageClean")}</p>
      ) : (
        <ul className="ob-live-rows">
          {rows.map((row) => (
            // data-kind carries the row's role to the stylesheet. The label
            // beside it says the same thing in words, so the colour it selects
            // is never the only signal.
            <li className="ob-live-coverage" data-kind={row.kind} key={row.id}>
              <span className="ob-live-coverage-label">{row.label}</span>
              {row.name !== undefined && (
                <span className="ob-live-coverage-name">{row.name}</span>
              )}
              {row.url !== undefined && (
                <span className="ob-live-coverage-url">{row.url}</span>
              )}
              <span className="ob-live-coverage-reason">{row.reason}</span>
            </li>
          ))}
        </ul>
      )}
    </DossierCard>
  );
}
