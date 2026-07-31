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
          title: SETTLED.has(scan.read.status)
            ? t("ob.scan.doneTitle", { host: scan.host })
            : t("ob.scan.title", { host: scan.host }),
          sub: SETTLED.has(scan.read.status)
            ? t("ob.scan.doneSub", {
                facts: counts.format(scan.read.facts.length),
                fields: counts.format(scan.read.profile_fields.length),
              })
            : t("ob.scan.sub"),
        };
  return (
    // The stage, not the column, is the snippet layer's containing block: the
    // chips belong in the page margin BESIDE the 540px column, so positioning
    // them against the column itself would confine the layer to the one strip of
    // the page it has to stay out of.
    <div className="ob-gate-stage">
      {scan === undefined ? null : <FactSnippets facts={scan.read.facts} />}
      <div className={`ob-gate${scan === undefined ? "" : " ob-scan"}`}>
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

  return (
    <>
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
    </>
  );
}

// Fixed positions rather than a random walk, and a step coprime with the slot
// count so consecutive facts always land in different slots: the sequence is
// then the same in a story, in a test and on screen. `Math.random()` in a
// render path is a surface nobody can pin.
const SNIPPET_SLOTS = 7;
const SLOT_STEP = 3;
const SNIPPET_CHARS = 32;

/**
 * A fact's identity as the server defines it. `value_key` alone is not one: it
 * is empty for every single-value fact (a phone number, a founding year), so
 * keying chips on it collides them, and React then reuses a chip that is
 * already mid-fade instead of mounting a new one — the fact silently never
 * appears.
 */
function snippetKey(fact: CompanySiteReadFact): string {
  return `${fact.category}/${fact.field}/${fact.value_key}`;
}

type LiveSnippet = Readonly<{
  key: string;
  fact: CompanySiteReadFact;
  slot: number;
}>;

// Which slot a newly arrived chip takes: the next one clear of every chip still
// on screen, walking from a cursor that advances by a step coprime with the slot
// count. Consecutive facts therefore land far apart, and the sequence is the
// same in a story, in a test and on screen — `Math.random()` in a render path
// is a surface nobody can pin. Returns null when the layer is full, and the
// fact is then simply not shown: this is decoration over the column, and the
// review that follows states every fact anyway.
function freeSlot(live: readonly LiveSnippet[], cursor: number): number | null {
  const taken = new Set(live.map((snippet) => snippet.slot));
  for (let step = 0; step < SNIPPET_SLOTS; step += 1) {
    const slot = (cursor + step * SLOT_STEP) % SNIPPET_SLOTS;
    if (!taken.has(slot)) {
      return slot;
    }
  }
  return null;
}

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
 *
 * A chip leaves when its OWN animation ends, not when the next poll arrives.
 * Extraction lands in per-page batches, so deriving the layer from a window
 * over `facts` would cut chips off mid-fade on the common path — and a batch of
 * four would replace the whole layer in a single frame.
 */
export function FactSnippets({
  facts,
}: Readonly<{ facts: readonly CompanySiteReadFact[] }>) {
  const reduced = usePrefersReducedMotion();
  const [live, setLive] = useState<readonly LiveSnippet[]>([]);
  // Facts already given their turn. A chip that has faded out must not come
  // back on the next poll, so admission is once per fact for the whole read.
  const admitted = useRef<Set<string>>(new Set());
  const cursor = useRef(0);
  const layer = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (reduced) {
      return;
    }
    let next = live;
    for (const fact of facts) {
      const key = snippetKey(fact);
      if (admitted.current.has(key)) {
        continue;
      }
      const slot = freeSlot(next, cursor.current);
      if (slot === null) {
        // The layer is full. Leave this fact and the ones behind it unadmitted,
        // in order, so a later pass can still surface them once a chip ends.
        break;
      }
      admitted.current.add(key);
      cursor.current = (cursor.current + SLOT_STEP) % SNIPPET_SLOTS;
      next = [...next, { key, fact, slot }];
    }
    if (next !== live) {
      setLive(next);
    }
    // The layer itself is a dependency, not just `facts`: a chip ending frees a
    // slot, and that is what gives a held-back fact its turn. This cannot loop —
    // a pass that admits nothing calls no setter, and `admitted` only grows.
  }, [facts, reduced, live]);

  const showing = !reduced && live.length > 0;

  // One delegated native listener on the layer rather than a handler per chip.
  // Native for the reason artifact.tsx documents — and here for a second one:
  // React does not deliver `animationend` to a JSX handler under jsdom, so a
  // handler prop would make the chip's whole retirement path untestable.
  useEffect(() => {
    const root = layer.current;
    if (!showing || !root) {
      return;
    }
    const retire = (event: Event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) {
        return;
      }
      const key = target.dataset.snipKey;
      if (key === undefined) {
        return;
      }
      setLive((current) => current.filter((held) => held.key !== key));
    };
    root.addEventListener("animationend", retire);
    return () => root.removeEventListener("animationend", retire);
  }, [showing]);

  if (!showing) {
    return null;
  }
  return (
    <div className="ob-snips" aria-hidden="true" ref={layer}>
      {live.map((snippet) => {
        const path = sourcePath(snippet.fact.evidence_url);
        return (
          <span
            key={snippet.key}
            className="ob-snip"
            data-snip-key={snippet.key}
            data-slot={snippet.slot}
            data-fact-category={snippet.fact.category}
          >
            <span className="ob-snip-value">
              {shortValue(snippet.fact.value)}
            </span>
            {path === null ? null : <span className="ob-snip-src">{path}</span>}
          </span>
        );
      })}
    </div>
  );
}
