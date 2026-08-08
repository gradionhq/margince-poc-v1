// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { components } from "../api/schema";
import { Disclosure } from "../design-system/atoms";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { normalizeUrl, skipReasonText } from "./onboarding";
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

/**
 * A read in flight, as the column needs it. One optional object rather than
 * three optional props: a read without the host it is reading, or without the
 * locale its counts are formatted in, is not a state this surface has.
 */
type GateScan = Readonly<{
  read: CompanySiteRead;
  host: string;
  locale: string;
}>;
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
/**
 * Why the reader is at the gate rather than watching a read. The tone is what
 * decides whether it announces as an alert or a status, so a resumed setup can
 * never interrupt like a failure: only `error` is something that went wrong.
 */
export type GateNotice = Readonly<{
  tone: "error" | "paused" | "resumed";
  message: string;
}>;

export function OnboardingGate({
  name,
  running,
  notice,
  configuredModel,
  scan,
  onSubmit,
  onManual,
}: Readonly<{
  name?: string;
  running: boolean;
  notice?: GateNotice;
  configuredModel: string;
  /**
   * The read this column is watching, once one is running. Present or absent as
   * a whole — the three values are only meaningful together, so there is no
   * state where the column has a read but not the host it is reading.
   */
  scan?: GateScan;
  onSubmit: (host: string) => void;
  onManual: () => void;
}>) {
  const t = useT();
  const [website, setWebsite] = useState("");
  const [invalid, setInvalid] = useState(false);
  const named = name?.trim();

  // The read replaces the tail of the SAME column rather than a second screen
  // replacing this one — see GateColumn for why that is load-bearing.
  if (scan !== undefined) {
    return (
      <GateColumn scan={scan}>
        <TheatreTail
          read={scan.read}
          locale={scan.locale}
          configuredModel={configuredModel}
        />
      </GateColumn>
    );
  }

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
    <GateColumn running={running} name={named}>
      <form className="ob-gate-form" onSubmit={submit}>
        <label className="sr-only" htmlFor="ob-gate-website">
          {t("ob.gate.field")}
        </label>
        {/* The border and the focus ring sit on the WRAPPER, so the field and
            its inline submit share one outline instead of drawing two. The
            stylesheet reads the input's own `aria-invalid` through `:has()` to
            colour that outline, which is why rejection is not mirrored onto a
            second attribute here: one source, and it is the one assistive
            technology already reads. */}
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

      {/* Withheld while a start is in flight, so choosing the manual path cannot
          race a read that is already beginning. */}
      {running ? null : (
        <p className="ob-gate-alt">
          {t("ob.gate.altPrompt")}
          <button type="button" className="ob-gate-link" onClick={onManual}>
            {t("ob.gate.altAction")}
          </button>
        </p>
      )}

      {/* The promise the product makes about the read, one press away rather
          than in the sentence under the headline. Everyone is entitled to it;
          almost nobody needs it before typing a domain, and carrying it in the
          opening paragraph is what made that paragraph four lines long. */}
      <Disclosure summary={t("ob.gate.trustToggle")}>
        <p className="ob-gate-trust">{t("ob.gate.trustBody")}</p>
      </Disclosure>

      {/* Named BEFORE the reader hands over their website, not after: which
          model is about to read it is part of the decision to let it. */}
      <p className="ob-gate-ai">
        <span>{t("ob.scan.transparency")}</span>
        <b>{configuredModel}</b>
      </p>
    </GateColumn>
  );
}

/**
 * The column both faces share, and the reason they are one component.
 *
 * The Core, the headline and the promise sentence keep their positions in the
 * tree from the first question through to the finished read; only the tail below
 * them is replaced. Rendering the gate and the theatre as two components at the
 * same position would make them different types there, so React would unmount
 * one subtree and mount the other: the Core would tear down and rebuild its
 * WebGL context, its float, breathe and sheen loops would all restart from
 * phase 0, and the entrance would replay. The most important moment in the flow
 * would flash and re-enter instead of continuing.
 */
function GateColumn({
  scan,
  running,
  name,
  children,
}: Readonly<{
  scan?: GateScan;
  running?: boolean;
  name?: string;
  children: ReactNode;
}>) {
  const t = useT();
  const counts = useMemo(
    () => new Intl.NumberFormat(scan?.locale),
    [scan?.locale],
  );
  const settled = scan !== undefined && SETTLED.has(scan.read.status);
  const head: Readonly<{
    core: MarginceCoreState;
    title: string;
    sub: string;
  }> =
    scan === undefined
      ? {
          core: running === true ? "working" : "listening",
          title: name
            ? t("ob.gate.title", { name })
            : t("ob.gate.titleAnonymous"),
          sub: t("ob.gate.sub"),
        }
      : {
          core: coreStateFor(scan.read.status),
          title: settled
            ? t("ob.scan.doneTitle", { host: scan.host })
            : t("ob.scan.title", { host: scan.host }),
          sub: settled
            ? t("ob.scan.doneSub", {
                facts: counts.format(scan.read.facts.length),
                fields: counts.format(scan.read.profile_fields.length),
              })
            : t("ob.scan.sub"),
        };
  return (
    <div className="ob-gate-stage">
      <div
        className={`ob-gate${scan === undefined ? "" : " ob-scan"}${
          settled ? " is-settled" : ""
        }`}
      >
        <MarginceCoreScene state={head.core} />
        <h1 className="ob-gate-title">{head.title}</h1>
        <p className="ob-gate-sub">{head.sub}</p>
        {children}
      </div>
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

function reasonOf(t: Translate, page: CompanySiteReadPage): string {
  return skipReasonText(t, page.reason);
}

// Colour is never the only carrier: the tile's own name says what happened to
// the page and why, and it is both the tooltip and the accessible name.
function pageLabel(t: Translate, page: CompanySiteReadPage): string {
  const reason = reasonOf(t, page);
  if (page.status === "skipped") {
    return t("ob.scan.pageSkipped", { url: page.url, reason });
  }
  if (page.status === "failed") {
    return t("ob.scan.pageFailed", { url: page.url, reason });
  }
  return t("ob.scan.pageFetched", { url: page.url });
}

// The ticker's own status word, with no URL in it: the path beside it is
// already the URL, spelled once. Reusing pageLabel here printed the URL a
// second time, in full, right next to its own truncated abbreviation.
function pageStatusWord(t: Translate, page: CompanySiteReadPage): string {
  const reason = reasonOf(t, page);
  if (page.status === "skipped") {
    return t("ob.scan.pageStatusSkipped", { reason });
  }
  if (page.status === "failed") {
    return t("ob.scan.pageStatusFailed", { reason });
  }
  return t("ob.scan.pageStatusFetched");
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
  return (
    <GateColumn scan={{ read, host, locale }}>
      <TheatreTail
        read={read}
        locale={locale}
        configuredModel={configuredModel}
      />
    </GateColumn>
  );
}

// The theatre's own regions — everything below the head the column already
// draws. Split out so the gate can swap this in WITHOUT replacing the column
// around it; GateColumn documents why that matters.
function TheatreTail({
  read,
  locale,
  configuredModel,
}: Readonly<{
  read: CompanySiteRead;
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
  const latestPage = useLatestArrivedPage(read.pages);

  return (
    <>
      {/* Fixed height, opacity-only crossfade: the phase changes in place. */}
      <p className="ob-scan-phase" aria-live="polite">
        <span className="ob-scan-phase-dot" aria-hidden />
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

      {/* The page itself: which one the crawl just walked, not a growing
          transcript of all of them. Fixed height, one occupant at a time —
          the old page fades out as the new one fades in, and under reduced
          motion the text simply swaps. If pages arrive faster than the fade
          can play, this shows whichever is newest and drops the rest rather
          than queuing a backlog: the count beside it is the honest tally,
          not a promise that every page was seen crossing the ticker. */}
      <div className="ob-scan-ticker">
        <ul
          className="ob-scan-ticker-row"
          aria-live="polite"
          aria-label={t("ob.scan.logLabel")}
        >
          {latestPage === null ? null : (
            <ScanTickerEntry key={latestPage.url} page={latestPage} t={t} />
          )}
        </ul>
        <p className="ob-scan-ticker-total">
          {t("ob.scan.pagesRead", { pages: counts.format(pagesRead) })}
        </p>
      </div>

      <p className="ob-scan-counts">
        <span>
          {t("ob.scan.pagesSkipped", { count: counts.format(skipped) })}
        </span>
        <span className="ob-scan-found">
          {t("ob.scan.factsSoFar", { count: counts.format(read.facts.length) })}
        </span>
        {settled ? null : <span>{t("ob.scan.stillReading")}</span>}
      </p>

      {/* The AI indigo, not the brand accent: this is what the machine spent,
          not something the user is being asked to do. The spend and call
          count are the point, so they sit together on the label's own line;
          which model(s) answered is detail, given its own full-width row
          beneath rather than a column it would have to be squeezed into. */}
      <div className="ob-scan-cost">
        <div className="ob-scan-cost-head">
          <p className="ob-scan-cost-label">{t("ob.scan.transparency")}</p>
          <p className="ob-scan-cost-line">
            {runtime === undefined ? (
              t("ob.scan.costPending")
            ) : (
              <>
                {costLine(t, runtime, locale)}
                {/* A call the provider billed with no effective rate is missing
                    from the total above, not folded into it as a silent zero —
                    so the figure reads as complete unless this says otherwise. */}
                {runtime.unpriced_calls > 0 && (
                  <small className="ob-scan-cost-unpriced">
                    {t("ob.scan.costUnpriced")}
                  </small>
                )}
              </>
            )}
          </p>
        </div>
        <p className="ob-scan-cost-model">{configuredModel}</p>
      </div>
    </>
  );
}

/**
 * The page the ticker should say was "just walked".
 *
 * The wire's `pages` array carries no arrival order: the transport lists every
 * fetched page before any skipped or failed one regardless of when either
 * actually happened, so the last array entry is not the newest page — once a
 * single page is skipped, `.at(-1)` would keep naming it forever, even while
 * later pages keep arriving. What IS honest is which URLs are new since the
 * last poll: this hook diffs against the URLs it has already shown and only a
 * genuinely new arrival replaces the ticker. Between polls with no new page it
 * holds what it last showed rather than re-deriving a "latest" from position.
 *
 * A single poll can still deliver more than one new URL at once — a fresh
 * fetch and a fresh skip together — and the wire's fetched-first ordering
 * means the skip is always last among them, not because it happened later.
 * A fetched page is real, useful news; a skip or failure is "nothing to see
 * here". So among the newly-arrived URLs this hook prefers the latest fetched
 * one, and only falls back to a skip/failure when nothing in the batch
 * actually fetched — rather than guess which of several simultaneous
 * arrivals is truly newest.
 */
function useLatestArrivedPage(
  pages: readonly CompanySiteReadPage[],
): CompanySiteReadPage | null {
  const seen = useRef<Set<string>>(new Set());
  const latest = useRef<CompanySiteReadPage | null>(null);
  const arrived = pages.filter((page) => !seen.current.has(page.url));
  const arrivedFetched = arrived.filter((page) => page.status === "fetched");
  const next = arrivedFetched.at(-1) ?? arrived.at(-1) ?? latest.current;
  useEffect(() => {
    for (const page of pages) {
      seen.current.add(page.url);
    }
    latest.current = next;
  });
  return next;
}

// The ticker's status word, reachable in full even though it visually clamps
// to two lines: `aria-label` carries the whole sentence regardless of what
// CSS clips (browsers disagree on how much of a `-webkit-line-clamp`d text
// node survives into the accessible name, so this never depends on that),
// and a plain button — rather than `title`, which needs a mouse hover no
// touch surface offers — lets a sighted reader expand the clamp on tap or
// keyboard activation. Toggling only flips a CSS class: the text node itself
// never changes, so it cannot re-trigger the ticker row's own `aria-live`
// announcement, which fires once, on this whole entry mounting for a new
// page — never on the reader expanding an old one.
function ScanTickerEntry({
  page,
  t,
}: Readonly<{ page: CompanySiteReadPage; t: Translate }>) {
  const [expanded, setExpanded] = useState(false);
  const reason = pageStatusWord(t, page);
  return (
    <li className="ob-scan-ticker-entry" data-page-status={page.status}>
      <span className="ob-scan-ticker-path">{sourcePath(page.url) ?? "/"}</span>
      <button
        type="button"
        className="ob-scan-ticker-kind"
        aria-expanded={expanded}
        aria-label={reason}
        onClick={() => setExpanded((current) => !current)}
      >
        {reason}
      </button>
    </li>
  );
}

// The page a crawled URL is, as the site's own path. Parsed with a pattern
// rather than `new URL`, so a malformed URL yields "no path to show" instead
// of an exception to swallow.
function sourcePath(url: string): string | null {
  const match = /^[a-z][a-z0-9+.-]*:\/\/[^/?#]+(\/[^?#]*)?/i.exec(url);
  const path = match?.[1];
  if (path === undefined || path === "" || path === "/") {
    return null;
  }
  return path.replace(/\/$/, "");
}
