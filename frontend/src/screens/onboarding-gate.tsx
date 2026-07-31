// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { type FormEvent, useMemo, useState } from "react";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { usePrefersReducedMotion } from "../design-system/motion";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { normalizeUrl } from "./onboarding";
import "./onboarding-gate.css";

// The first screen of onboarding: one question, then the wait for the website
// read made worth watching.
//
// Two rules shape everything here. The surface is PROP-DRIVEN — no fetch, no
// router, no clock deciding what the UI claims — so the read's progress can only
// ever be what the polled `CompanySiteRead` actually says. And every number is
// an OPEN count: the wire carries no page-count denominator, so there is no
// fraction, no percentage and no bar with a known end to be drawn from it.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];
type CompanySiteReadPage = components["schemas"]["CompanySiteReadPage"];
type AiRunSummary = components["schemas"]["AiRunSummary"];

type Translate = ReturnType<typeof useT>;

/**
 * The ask: name, promise, one field.
 *
 * `running` covers the moment between the submit and the server handing back a
 * read — the Core answers, the submit stops accepting a second press. Once the
 * read exists the caller swaps this for `ReadTheatre`.
 */
/**
 * Why an earlier attempt is not on screen any more.
 *
 * `message` is a finished sentence, composed by the caller. The gate cannot
 * compose it itself: the reasons come from four different places (a failed
 * POST, a terminal read, a lost poll, a restore recap) and each already has its
 * own complete copy in the catalog. Wrapping one sentence inside another is how
 * you end up with "I could not start the read: I could not read that site."
 *
 * Two tones, because these are not the same news and must not read the same:
 * `error` means the read cannot happen as asked, `paused` means the server
 * shelved it and will come back to it. Rendering a deferral as a failure would
 * tell the reader to fix something that is not broken. The tone drives the
 * live-region role too, so the difference survives without colour.
 */
export type GateNotice = Readonly<{
  tone: "error" | "paused";
  message: string;
}>;

export function OnboardingGate({
  name,
  running,
  notice,
  configuredModel,
  onSubmit,
  onManual,
}: Readonly<{
  name?: string;
  running: boolean;
  notice?: GateNotice;
  configuredModel: string;
  onSubmit: (host: string) => void;
  onManual: () => void;
}>) {
  const t = useT();
  const [website, setWebsite] = useState("");
  const [invalid, setInvalid] = useState(false);
  const named = name?.trim();

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    // Guarded in the handler as well as the attribute: Enter reaches the form
    // even while the button is disabled.
    if (running) {
      return;
    }
    const target = normalizeUrl(website);
    if (!target.ok) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    onSubmit(target.host);
  };

  return (
    <div className="ob-gate">
      <MarginceCoreScene state={running ? "working" : "listening"} />
      <h1 className="ob-gate-title">
        {named
          ? t("ob.gate.title", { name: named })
          : t("ob.gate.titleAnonymous")}
      </h1>
      <p className="ob-gate-sub">{t("ob.gate.sub")}</p>

      <form className="ob-gate-form" onSubmit={submit}>
        <label className="sr-only" htmlFor="ob-gate-website">
          {t("ob.gate.field")}
        </label>
        {/* The border and the focus ring sit on the WRAPPER, so the field and
            its inline submit share one outline instead of drawing two. */}
        <div className="ob-gate-field">
          <input
            id="ob-gate-website"
            className="ob-gate-input"
            type="text"
            inputMode="url"
            autoComplete="url"
            spellCheck={false}
            placeholder={t("ob.gate.placeholder")}
            value={website}
            aria-invalid={invalid}
            aria-describedby={invalid ? "ob-gate-invalid" : undefined}
            onChange={(event) => {
              setWebsite(event.target.value);
              setInvalid(false);
            }}
          />
          <button
            type="submit"
            className="ob-gate-submit"
            disabled={running}
            aria-busy={running}
          >
            {t("ob.gate.submit")}
          </button>
        </div>
      </form>

      {invalid ? (
        <p className="ob-gate-alert" id="ob-gate-invalid" role="alert">
          {t("ob.gate.invalidUrl")}
        </p>
      ) : null}
      {notice === undefined ? null : (
        <p
          className={`ob-gate-alert is-${notice.tone}`}
          role={notice.tone === "error" ? "alert" : "status"}
        >
          {notice.message}
        </p>
      )}

      <p className="ob-gate-alt">
        {t("ob.gate.altPrompt")}
        <button type="button" className="ob-gate-link" onClick={onManual}>
          {t("ob.gate.altAction")}
        </button>
      </p>

      {/* Named BEFORE the reader hands over their website, not after: which
          model is about to read it is part of the decision to let it. */}
      <p className="ob-gate-ai">
        <span>{t("ob.scan.transparency")}</span>
        <b>{configuredModel}</b>
      </p>
    </div>
  );
}

// A read that has produced its answer. `partial` belongs here: it carries facts
// and fields, it is simply missing pages the crawl could not reach — which the
// page strip already states, tile by tile.
const SETTLED: ReadonlySet<CompanySiteRead["status"]> = new Set([
  "ready",
  "partial",
  "confirmed",
]);
const BROKEN: ReadonlySet<CompanySiteRead["status"]> = new Set([
  "failed",
  "abandoned",
]);

function coreStateFor(status: CompanySiteRead["status"]): MarginceCoreState {
  if (SETTLED.has(status)) {
    return "success";
  }
  return BROKEN.has(status) ? "error" : "working";
}

// The one phase line, from the only two fields that carry a phase. `status`
// wins over `phase` because a queued or deferred read has not started, whatever
// a stale `phase` from an earlier attempt still says. No fifth message is
// invented for the states that carry no phase: the line goes empty instead.
function phaseKey(read: CompanySiteRead): MessageKey | null {
  if (read.status === "queued") {
    return "ob.scan.phaseQueued";
  }
  if (read.status === "deferred") {
    return "ob.scan.phaseDeferred";
  }
  if (read.phase === "crawling") {
    return "ob.scan.phaseCrawling";
  }
  if (read.phase === "extracting") {
    return "ob.scan.phaseExtracting";
  }
  return null;
}

// Colour is never the only carrier: the tile's own name says what happened to
// the page and why, and it is both the tooltip and the accessible name.
function pageLabel(t: Translate, page: CompanySiteReadPage): string {
  const reason =
    page.reason !== null &&
    page.reason !== undefined &&
    page.reason.trim() !== ""
      ? page.reason
      : t("ob.scan.pageNoReason");
  if (page.status === "skipped") {
    return t("ob.scan.pageSkipped", { url: page.url, reason });
  }
  if (page.status === "failed") {
    return t("ob.scan.pageFailed", { url: page.url, reason });
  }
  return t("ob.scan.pageFetched", { url: page.url });
}

function costLine(t: Translate, runtime: AiRunSummary, locale: string): string {
  const counts = new Intl.NumberFormat(locale);
  const money = new Intl.NumberFormat(locale, {
    style: "currency",
    currency: runtime.currency,
    // A read costs fractions of a cent, and two decimals would round every
    // honest disclosure down to zero.
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  });
  return t("ob.scan.costLine", {
    calls: counts.format(runtime.call_attempts),
    tokens: counts.format(runtime.tokens_in + runtime.tokens_out),
    cost: money.format(runtime.estimated_cost_microusd / 1_000_000),
  });
}

/**
 * The wait: what is happening, what has been read, what it cost.
 *
 * Everything on screen is a field of the polled read. The three regions each
 * hold their own size so an arriving page or a new phase never re-lays out the
 * column.
 */
export function ReadTheatre({
  read,
  host,
  locale,
  configuredModel,
}: Readonly<{
  read: CompanySiteRead;
  host: string;
  locale: string;
  configuredModel: string;
}>) {
  const t = useT();
  const counts = useMemo(() => new Intl.NumberFormat(locale), [locale]);
  const settled = SETTLED.has(read.status);
  const phase = phaseKey(read);
  const runtime = read.ai_runtime;
  // `pages_read` is the server's own tally; where it is absent the fetched
  // tiles are the same fact counted from the array rather than a guess.
  const pagesRead =
    read.pages_read ??
    read.pages.filter((page) => page.status === "fetched").length;
  const skipped = read.pages.filter((page) => page.status === "skipped").length;

  return (
    <div className="ob-gate ob-scan">
      <FactSnippets facts={read.facts} />
      <MarginceCoreScene state={coreStateFor(read.status)} />
      <h1 className="ob-gate-title">
        {settled
          ? t("ob.scan.doneTitle", { host })
          : t("ob.scan.title", { host })}
      </h1>
      <p className="ob-gate-sub">
        {settled
          ? t("ob.scan.doneSub", {
              facts: counts.format(read.facts.length),
              fields: counts.format(read.profile_fields.length),
            })
          : t("ob.scan.sub")}
      </p>

      {/* Fixed height, opacity-only crossfade: the phase changes in place. */}
      <p className="ob-scan-phase" aria-live="polite">
        {phase === null ? null : (
          <span key={phase} className="ob-scan-phase-text">
            {t(phase)}
          </span>
        )}
      </p>

      <ul className="ob-scan-strip" aria-label={t("ob.scan.pageStripLabel")}>
        {read.pages.map((page) => {
          const label = pageLabel(t, page);
          return (
            <li key={page.url}>
              <span
                className="ob-scan-tile"
                data-page-status={page.status}
                role="img"
                title={label}
                aria-label={label}
              />
            </li>
          );
        })}
      </ul>

      <p className="ob-scan-counts">
        <span>
          {t("ob.scan.pagesRead", { pages: counts.format(pagesRead) })}
        </span>
        <span>
          {t("ob.scan.pagesSkipped", { count: counts.format(skipped) })}
        </span>
        <span>
          {t("ob.scan.factsSoFar", { count: counts.format(read.facts.length) })}
        </span>
        {settled ? null : <span>{t("ob.scan.stillReading")}</span>}
      </p>

      {/* The AI indigo, not the brand accent: this is what the machine spent,
          not something the user is being asked to do. */}
      <div className="ob-scan-cost">
        <p className="ob-scan-cost-label">{t("ob.scan.transparency")}</p>
        <p className="ob-scan-cost-model">{configuredModel}</p>
        <p className="ob-scan-cost-line">
          {runtime === undefined
            ? t("ob.scan.costPending")
            : costLine(t, runtime, locale)}
        </p>
      </div>
    </div>
  );
}

// Fixed positions rather than a random walk, and a step coprime with the slot
// count so consecutive facts always land in different slots: the sequence is
// then the same in a story, in a test and on screen. `Math.random()` in a
// render path is a surface nobody can pin.
const SNIPPET_SLOTS = 7;
const SLOT_STEP = 3;
// How many chips share the layer. Below the slot count, so no two on screen can
// ever collide; each leaves the DOM as the facts array grows past it, which is
// what ends its animation — no timer decides anything here.
const SNIPPET_WINDOW = 4;
const SNIPPET_CHARS = 32;

function shortValue(value: string): string {
  const trimmed = value.trim();
  if (trimmed.length <= SNIPPET_CHARS) {
    return trimmed;
  }
  return `${trimmed.slice(0, SNIPPET_CHARS - 1).trimEnd()}…`;
}

// The page a fact came from, as the site's own path. Parsed with a pattern
// rather than `new URL`, so a malformed evidence URL yields "no path to show"
// instead of an exception to swallow.
function sourcePath(evidenceUrl: string): string | null {
  const match = /^[a-z][a-z0-9+.-]*:\/\/[^/?#]+(\/[^?#]*)?/i.exec(evidenceUrl);
  const path = match?.[1];
  if (path === undefined || path === "" || path === "/") {
    return null;
  }
  return path.replace(/\/$/, "");
}

/**
 * Facts surfacing in place while the read runs — decoration over the column,
 * and `aria-hidden` because every fact is stated in the review that follows and
 * every count is already text in the panel above.
 *
 * Nothing travels: a chip fades in where it belongs, holds long enough to be
 * read, and fades out. Under reduced motion there is nothing to show in place
 * of an animation whose whole content IS the animation, so the layer stands
 * down; the counts and the page strip carry the same information without it.
 */
export function FactSnippets({
  facts,
}: Readonly<{ facts: readonly CompanySiteReadFact[] }>) {
  const reduced = usePrefersReducedMotion();
  if (reduced || facts.length === 0) {
    return null;
  }
  const first = Math.max(0, facts.length - SNIPPET_WINDOW);
  return (
    <div className="ob-snips" aria-hidden="true">
      {facts.slice(first).map((fact, offset) => {
        const path = sourcePath(fact.evidence_url);
        return (
          <span
            key={fact.value_key}
            className="ob-snip"
            data-slot={((first + offset) * SLOT_STEP) % SNIPPET_SLOTS}
            data-fact-category={fact.category}
          >
            <span className="ob-snip-value">{shortValue(fact.value)}</span>
            {path === null ? null : <span className="ob-snip-src">{path}</span>}
          </span>
        );
      })}
    </div>
  );
}
