// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import {
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { formatMoney, INTL_LOCALE } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useOrganization360 } from "../screens/company360";
import { useConnectors } from "../screens/connectors";
import { useDedupeQueue } from "../screens/dedupe";
import { usePendingApprovals } from "../screens/inbox.queries";
import { useLicenseEntitlement } from "../screens/license";
import { type AppActivity, useAppActivity } from "./activity";
import { type AgentActivity, useAgentActivity } from "./agent-activity";
import { lineFor, PANEL_HEADING } from "./agent-activity-lines";
import {
  IDLE_ORDER,
  type IdleKind,
  LABELS,
  REVIEW_ONLY,
  RUNNING,
  TASK_SAID,
  VOCABULARY,
} from "./agentrail-copy";
import { type DemoRun, useDemoRun } from "./agentrail-demo";
import { useAgentTicker } from "./agentrail-ticker";
import { useAgentTierMap } from "./autonomy";
import { useCan } from "./capability";
import { usePopoverDismiss } from "./popover";
import type { Route } from "./router";
import {
  announceAgentStatesPreview,
  uiPreviewAgentStatesEnabled,
} from "./ui-preview";
import { usePhoneViewport } from "./viewport";
import "./agentrail.css";

// The agent's place in the app: the foot of the workspace rail, under the
// destinations, carrying the Core as its status light.
//
// This is the ONE agent surface. Two others were built and judged first, a dock
// beside the page title and a bar across the foot of the viewport, and both put
// the agent in the content column, which is the column the reader is working in.
// Both then had to answer a question every other always-present thing answers
// with the rail: an agent that is always there belongs in the chrome that is
// always there.
//
// The rail also settles the SPLIT the bar was built to argue for. What the agent
// has on the page you are standing on, and what it is doing everywhere else, are
// two readings of different scope, and a horizontal bar spent its whole width
// keeping them apart. Stacked in a column they are simply two lines, and the
// panel that opens beside them carries the detail of both.
//
// EVERYTHING IT REPORTS IS READ FROM THE API: approvals waiting, which sources
// are unreachable, the model the last call actually ran on, this account's own
// suggestions. Nothing is a zero standing in for a read that has not answered:
// a row whose read is pending, or that this seat may not make, is absent
// instead. The one invented table left is `REVIEW_ONLY` (agentrail-copy.ts),
// the states no read can reach, offered from the switcher in the panel under a
// heading that says review-only. The rail never enters one of those on its own.

/** How many of the agent's last actions the panel recaps. */
const RECAP_ROWS = 5;

/** How much dimmer each row's mark is than the one above it. */
const MARK_FADE = 0.16;

/** Where the whole trace lives, and where a model gets bound. Same tab. */
const AI_SETTINGS_HREF = "#/settings/ai";

/** What the installation can actually tell us, and what it cannot. */
type Signals = Readonly<{
  /** Approvals staged for this human; undefined until the read answers. */
  waiting: number | undefined;
  /** Sources the agent cannot reach, named as the reader knows them. */
  offline: readonly string[];
  /** Duplicate pairs the agent will not decide for itself; undefined until read. */
  duplicates: number | undefined;
  /** Whether this deployment has a model bound at all. */
  ai: AiPosture;
  /** What the installation is entitled to; undefined when this seat may not
   *  read it, which is not the same as an installation with no licence. */
  license: LicensePosture | undefined;
  /** The licence posture in the reader's words, for the line and the panel. */
  licenseLine: string;
}>;

/**
 * What the installation's entitlement adds up to, for a surface that reports
 * rather than enforces.
 *
 * `none` and `refused` are the two a person has to act on, and they are why the
 * Core carries this at all: an installation with no licence is not a healthy
 * agent with a footnote, it is a standing fault, and the rail used to state it
 * as a grey row at the very bottom that nobody read. `pressing` is the same
 * claim one step softer: over the seat cap, in grace, or renewal due.
 */
type LicensePosture = "ok" | "pressing" | "refused" | "none";

/**
 * What the deployment has bound, as `/assistant/profile` reports it.
 *
 * `configured` says the bindings were CONSTRUCTED at boot — the contract is
 * explicit that it is not a health check, so nothing here may render as online,
 * running or healthy. The negative is the honest half and the one worth showing:
 * a deployment with no provider key has an agent that cannot think, and every
 * other thing this bar reports is beside the point until that is fixed.
 */
type AiPosture = "configured" | "unconfigured" | "development" | "unknown";

/**
 * The reads the bar's right half stands on, and the state they add up to.
 *
 * Order is severity, not convenience: a source the agent cannot reach outranks a
 * queue, because a queue built from half the evidence is the more dangerous of
 * the two to report calmly. Everything else rests at `idle` — proposals
 * WAITING is not a state of its own, it is the agent at rest with a number
 * beside it, and that number is the thing a person acts on.
 */
function useAiPosture(): AiPosture {
  const profile = useQuery({
    queryKey: ["assistant-profile"],
    // Anonymous, cheap and effectively static for the life of the process: the
    // same key the sign-in screen uses, so the two share one answer.
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
    queryFn: async () => {
      const { data, error } = await api.GET("/assistant/profile");
      if (error) {
        return null;
      }
      return data;
    },
  });
  return profile.data?.state ?? "unknown";
}

function useSignals(): Signals {
  const t = useT();
  const approvals = usePendingApprovals();
  const connectors = useConnectors();
  const dedupe = useDedupeQueue();
  const ai = useAiPosture();
  const license = useLicensePosture();

  const offline = (connectors.data?.data ?? [])
    .filter((connection) => connection.status !== "connected")
    .map((connection) => connection.account_label ?? connection.provider);
  const duplicates = dedupe.data ? dedupe.data.data.length : undefined;

  return {
    // Absent `data` means the read has not answered, or was refused. A 0 here
    // would be this surface inventing an all-clear.
    waiting: approvals.data ? approvals.data.data.length : undefined,
    offline,
    duplicates,
    ai,
    license,
    licenseLine:
      license === "refused"
        ? t("shell.license.refused")
        : t("shell.license.none"),
  };
}

/**
 * The installation's entitlement, read the way the rail used to read it.
 *
 * Absent for a seat without `license:read`, silently: a read they may not make
 * is not a fact being withheld from them, it is a fact that is none of their
 * work, and an orb that went amber about it on every screen they opened would be
 * a permission boundary drawn as a fault.
 */
function useLicensePosture(): LicensePosture | undefined {
  const mayRead = useCan("license", "read");
  const query = useLicenseEntitlement(mayRead);
  const entitlement = query.data;
  if (!mayRead || !entitlement) {
    return undefined;
  }
  if (entitlement.state === "rejected") {
    return "refused";
  }
  if (entitlement.state !== "valid") {
    return "none";
  }
  return entitlement.over_limit ||
    entitlement.license?.in_grace === true ||
    entitlement.license?.renewal_due === true
    ? "pressing"
    : "ok";
}

/**
 * The model the agent last actually ran on — the SERVED one, not the configured
 * one, because a fallback ladder makes those two differ exactly when it matters.
 *
 * Operator-only: `/ai/calls` sits behind `automation:update`, so a sales seat
 * gets nothing and the runtime row says the seat cannot read it rather than
 * printing a model nobody on that seat could verify. One call, because the panel
 * shows one line.
 */
type AiCall = components["schemas"]["AiCallSummary"];

/**
 * The last few things the agent actually did, newest first.
 *
 * This is the recap the panel owes a reader: not what the agent IS, but what it
 * has been doing — the task, the model it ran on, when. The full trace, with the
 * attempt ladder and the payloads, lives on the AI settings tab, and the panel
 * links to it rather than reproducing it. A recap that grows into a log is a
 * second log to keep correct.
 *
 * Operator-only, because `/ai/calls` sits behind `automation:update`. A seat
 * without it gets no rows and is told why, rather than an empty section that
 * reads as an agent which has never run.
 */
function useRecentCalls(): Readonly<{
  allowed: boolean;
  calls: readonly AiCall[];
}> {
  const allowed = useCan("automation", "update");
  const recent = useQuery({
    queryKey: ["ai-calls", "agentrail-recent"],
    enabled: allowed,
    staleTime: 30_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/calls", {
        params: { query: { limit: RECAP_ROWS } },
      });
      if (error) {
        // Chrome must not take a page down over telemetry: an unreadable log is
        // a state this surface draws, not an error it throws.
        return [];
      }
      return data.data;
    },
  });
  return { allowed, calls: recent.data ?? [] };
}

/**
 * What the agent has cost this month, as the server priced it.
 *
 * `cost_est_minor` is an ESTIMATE the server computes on read from its own rate
 * tables, in minor units of the budget's currency, and it is omitted for a call
 * nothing could be priced against. So the sum is over the lines that HAVE a
 * price, and a month where nothing was priced draws no figure at all rather than
 * a confident zero: the difference between "this cost nothing" and "nobody knows
 * what this cost" is the whole point of the row.
 *
 * Operator-only, on the same grant the spend card uses: the server treats the
 * runtime's cost as operator information, so a sales seat gets nothing and the
 * panel says so rather than printing a number that seat could not verify.
 */
function useAiSpend(): Readonly<{
  allowed: boolean;
  minor: number | undefined;
  currency: string;
  /** One number per day of the month so far, oldest first. */
  daily: readonly number[];
}> {
  const allowed = useCan("automation", "update");
  const usage = useQuery({
    queryKey: ["ai-usage", "agentrail-month"],
    enabled: allowed,
    // The month's spend does not move between two page opens, and this read sits
    // in the chrome, so a short staleness would put a request behind every
    // navigation.
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/usage", {
        params: { query: {} },
      });
      if (error) {
        // Chrome must not take a page down over a reading: an unreadable figure
        // is a state this surface draws, not an error it throws.
        return null;
      }
      return data;
    },
  });
  const days = usage.data?.days ?? [];
  const priced = days
    .flatMap((day) => day.tasks)
    .filter((task) => task.cost_est_minor !== undefined);
  return {
    allowed,
    minor:
      priced.length === 0
        ? undefined
        : priced.reduce((total, task) => total + (task.cost_est_minor ?? 0), 0),
    currency: usage.data?.budget.currency ?? "USD",
    // A day with no priced call is a real zero here, unlike the total: the month
    // HAS that day, and a gap in a series is a lie about its shape.
    daily: days.map((day) =>
      day.tasks.reduce((total, task) => total + (task.cost_est_minor ?? 0), 0),
    ),
  };
}

/**
 * What the runtime row prints, in the three cases it genuinely has: the model
 * the last call was SERVED by — not the configured one, because a fallback
 * ladder makes those differ exactly when it matters — or the reason there is
 * none.
 */
function modelText(
  read: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>,
): string {
  const latest = read.calls[0];
  if (latest) {
    return `${latest.provider}/${latest.served_model}`;
  }
  return read.allowed ? LABELS.noCallsYet : LABELS.unreadable;
}

/**
 * What the agent has to say about the record you are on.
 *
 * The account's own suggestions, from the same 360 read the company page makes —
 * one query key, so the bar and the page can never disagree about what is
 * standing. Only organizations serve them today; every other screen gets the
 * honest empty line rather than an invented finding.
 */
/**
 * What the agent has on the record you are standing on, and whether it is
 * reading it right now.
 *
 * The same 360 query the company page makes — one key, so the two can never
 * disagree, and being on that page costs nothing extra because the page has
 * already asked. `reading` is the honest source for the bar's one moment of
 * `ingesting`: the app is literally fetching this record's evidence, so the orb
 * taking context in is a report and not a flourish.
 */
function useRecordRead(route: Route): Readonly<{ reading: boolean }> {
  const isCompany = route.screen === "companies" && Boolean(route.id);
  // Enabled, not id-juggled: an empty id is a request the server answers 422,
  // and chrome that fires one on every screen is chrome that logs an error on
  // every screen.
  const org = useOrganization360(route.id ?? "", isCompany);
  return { reading: isCompany && org.isFetching };
}

/**
 * The recap: what the agent has done lately, and the door to the whole trace.
 *
 * Five rows at most. The question a person asks of a background agent is "what
 * have you been doing", and five answers it — a sixth turns the panel into a log
 * viewer, which already exists and is better at it.
 */
/** The task in the reader's words, or the token opened up if it is a new one. */
function saidFor(task: string): string {
  // `task` comes off the wire, and a bare lookup answers `constructor` from the
  // prototype chain with a function that React then tries to render.
  return Object.hasOwn(TASK_SAID, task)
    ? TASK_SAID[task]
    : task.replaceAll("_", " ");
}

/**
 * When it happened, as a person would say it.
 *
 * A wall-clock stamp answers "at what time", and the question a recap answers is
 * "how long ago" — five rows of `19/08/2026, 10:00` make the reader do the
 * subtraction, five times, to learn that everything happened this morning.
 */
function agoFor(iso: string, locale: Locale, now: number): string {
  const seconds = Math.round((now - Date.parse(iso)) / 1000);
  if (Number.isNaN(seconds)) {
    return LABELS.justNow;
  }
  const format = new Intl.NumberFormat(INTL_LOCALE[locale], {
    style: "unit",
    unitDisplay: "narrow",
    maximumFractionDigits: 0,
    unit:
      seconds < 60
        ? "second"
        : seconds < 3600
          ? "minute"
          : seconds < 86_400
            ? "hour"
            : "day",
  });
  const size =
    seconds < 60 ? 1 : seconds < 3600 ? 60 : seconds < 86_400 ? 3600 : 86_400;
  return format.format(Math.max(0, Math.floor(seconds / size)));
}

function Recap({
  recent,
}: Readonly<{
  recent: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
}>) {
  const { locale } = useLocale();
  // Read once per open, so five rows share one reading of the clock and cannot
  // disagree about what "now" is.
  const now = Date.now();
  if (!recent.allowed) {
    return <p className="aritem arempty">{LABELS.logUnreadable}</p>;
  }
  if (recent.calls.length === 0) {
    return <p className="aritem arempty">{LABELS.noCallsYet}</p>;
  }
  return (
    <>
      {recent.calls.map((call, index) => (
        <p className="aritem" key={call.id}>
          {/* The mark fades down the list, so the newest thing the agent did is
              the brightest thing in it. Position IS the age here: the rows are
              newest first, and a reader takes the gradient before they read a
              single timestamp. */}
          <span
            className="armark"
            aria-hidden="true"
            style={{ opacity: Math.max(0.3, 1 - index * MARK_FADE) }}
          />
          {saidFor(call.task)}
          <span className="armuted">
            {agoFor(call.occurred_at, locale, now)}
          </span>
        </p>
      ))}
    </>
  );
}

function RuntimeRows({
  offline,
  model,
  ai,
  license,
  licenseLine,
}: Readonly<{
  offline: readonly string[];
  model: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
  ai: AiPosture;
  license: LicensePosture | undefined;
  licenseLine: string;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const spend = useAiSpend();
  const tools = Object.values(useAgentTierMap()).length;
  return (
    <div className="armeta">
      {/* The posture leads, because it decides whether anything below it means
          anything: a model name from last week is not a model bound today. */}
      {ai === "unconfigured" && (
        <span className="arwarn">{LABELS.noModel}</span>
      )}
      {ai === "development" && (
        <span>
          <b>{t("auth.coreDevelopment")}</b> {t("auth.coreModeDevelopment")}
        </span>
      )}
      <span>
        {LABELS.model}{" "}
        {model.calls.length > 0 ? (
          <b>{modelText(model)}</b>
        ) : (
          <i>{modelText(model)}</i>
        )}
      </span>
      {/* Absent when the seat may not read it, and absent again when nothing in
          the month carried a price. A spend row is the one figure on this panel
          somebody will quote at somebody else, so it is drawn only when the
          server actually priced the calls behind it. */}
      {spend.allowed && spend.minor !== undefined && (
        <span>
          {LABELS.spend}{" "}
          <b>{formatMoney(spend.minor, spend.currency, locale)}</b>
        </span>
      )}
      {tools > 0 && (
        <span>
          {LABELS.tools} <b>{tools}</b>
        </span>
      )}
      {(license === "none" || license === "refused") && (
        <span className="arwarn">{licenseLine}</span>
      )}
      {offline.map((source) => (
        <span className="arconn down" key={source}>
          <i aria-hidden="true" />
          {`${source} ${LABELS.offline}`}
        </span>
      ))}
    </div>
  );
}

/** One scheduled run, as the server reports it. */
type ActivityItem = components["schemas"]["ActivityItem"];

/**
 * One list of scheduled runs, in the reader's words, under its own heading.
 *
 * A kind or state the copy map has no line for draws NOTHING — not a fallback
 * sentence, not the message key. `lineFor` returning null is the map saying it
 * has never heard of this run, and a surface that answers that with an invented
 * sentence is a surface a reader cannot trust about the runs it DOES name. When
 * that empties the section, the section is absent too.
 *
 * `degrade_reason` is server-authored operator vocabulary and untranslated, so
 * it lives here as detail under its own label and never inside the line: a raw
 * internal token in a localized sentence is the defect, whichever sentence it
 * is.
 */
function RunSection({
  heading,
  items,
}: Readonly<{ heading: MessageKey; items: readonly ActivityItem[] }>) {
  const t = useT();
  // flatMap rather than map+filter: the empty array drops the run AND narrows
  // the line to a string, where a filtered predicate would only have claimed it.
  const said = items.flatMap((item) => {
    const line = lineFor(item, t);
    return line === null ? [] : [{ item, line }];
  });
  if (said.length === 0) {
    return null;
  }
  return (
    <div className="arsect">
      <h4>{t(heading)}</h4>
      <ul className="arruns">
        {said.map(({ item, line }) => (
          <li className="arrun" key={item.id}>
            <span className="arrunline">{line}</span>
            {item.degrade_reason ? (
              <span className="arrundetail">
                <i>{t(PANEL_HEADING.stopped)}</i>
                {item.degrade_reason}
              </span>
            ) : null}
            {item.summary ? (
              <span className="arrundetail">{item.summary}</span>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

function AgentPanel({
  state,
  setState,
  line,
  running,
  recent,
  signals,
  model,
  spend,
  panel,
  frame,
  demo,
}: Readonly<{
  state: MarginceCoreState;
  setState: (next: MarginceCoreState) => void;
  /** The same line the card carries, so the two never disagree. */
  line: string;
  /** The scheduled runs the server reports as live. */
  running: readonly ActivityItem[];
  /** The runs that settled since local midnight, newest first. */
  recent: readonly ActivityItem[];
  signals: Signals;
  model: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
  spend: Readonly<{
    allowed: boolean;
    minor: number | undefined;
    currency: string;
  }>;
  panel: RefObject<HTMLElement | null>;
  frame: PanelFrame;
  demo: DemoRun;
}>) {
  const { locale } = useLocale();
  const states = VOCABULARY;
  return (
    <section
      className="arpanel"
      ref={panel}
      aria-label={LABELS.panel}
      style={{
        left: frame.left,
        right: frame.right,
        bottom: frame.bottom,
        width: frame.width,
        maxHeight: frame.maxHeight,
      }}
    >
      {/* The head restates what the card said, because the panel opens over the
          page and away from it: a reader who came here for the detail should not
          have to look back at the rail to remember what the detail is about. */}
      {/* No orb here. The card in the rail already carries one, and a second
          Core a few pixels away is the same object drawn at another size against
          another ground: the two never quite agree, and a reader who sees them
          disagree stops trusting either. The state's own word and tone carry it
          instead. */}
      <header className="arphead">
        <span className="arpstate">
          <i aria-hidden="true" />
          {state}
        </span>
        <p className="arptitle">{line}</p>
        {spend.allowed && spend.minor !== undefined && (
          <span className="arpmoney">
            <b>{formatMoney(spend.minor, spend.currency, locale)}</b>
            <i>{LABELS.thisMonth}</i>
          </span>
        )}
      </header>

      {/* Above the counts: a run happening this second outranks a queue that has
          been waiting since yesterday, and what finished overnight outranks it
          too — it is the work the reader slept through. Settled runs are read
          HERE and nowhere else: the terminal states, and with them every
          `degrade_reason` and `summary`, exist only on a run that has finished.
          Either section is absent when its list is, rather than drawn empty. */}
      <RunSection heading={PANEL_HEADING.running} items={running} />
      <RunSection heading={PANEL_HEADING.settled} items={recent} />

      {/* The counts, as tiles rather than rows: they are the two numbers somebody
          opens this panel to act on, and a number in a list of rows reads as one
          more line of text. The strip carries whichever of them answered, which
          is anything from none to both. */}
      <div className="arsect">
        <h4>{LABELS.acrossWorkspace}</h4>
        {signals.waiting === undefined && signals.duplicates === undefined ? (
          // Nothing to act on, said in words. An absent block would read as a
          // panel that has not looked; this is the agent saying it looked.
          <p className="arnone">{LABELS.allClear}</p>
        ) : (
          <div className="artiles">
            {/* The tile leads with the number because that is what a reader
                scans for, and its NAME leads with the label because "10" spoken
                alone is not a sentence about anything. */}
            {signals.waiting !== undefined && (
              <a
                className="artile"
                href="#/inbox"
                aria-label={`${LABELS.approvals} ${signals.waiting}`}
              >
                <b>{signals.waiting}</b>
                <span>{LABELS.approvals}</span>
              </a>
            )}
            {signals.duplicates !== undefined && (
              <a
                className="artile"
                href="#/dedupe"
                aria-label={`${LABELS.duplicatesRow} ${signals.duplicates}`}
              >
                <b>{signals.duplicates}</b>
                <span>{LABELS.duplicatesRow}</span>
              </a>
            )}
          </div>
        )}
      </div>

      <div className="arsect">
        <h4>
          {LABELS.recap}
          <a className="arplain" href={AI_SETTINGS_HREF}>
            {LABELS.fullLog}
          </a>
        </h4>
        <Recap recent={model} />
      </div>

      {/* What it is standing on, in one strip. Not a section of its own: it is
          the small print of the panel, and small print that takes a heading and
          four rows reads as more important than the counts above it. */}
      <div className="arstrip">
        <RuntimeRows
          offline={signals.offline}
          model={model}
          ai={signals.ai}
          license={signals.license}
          licenseLine={signals.licenseLine}
        />
      </div>

      {/* Review-only, and behind a switch (app/ui-preview.ts): some of these
          states describe an overnight run no read can reach, so a control
          that enters them is a control over what the surface CLAIMS. It has no
          place in an installation. */}
      {uiPreviewAgentStatesEnabled() && (
        <div className="arsect arstates">
          <h4>{LABELS.states}</h4>
          <div className="archips">
            {/* The chips hold one state still; this plays the whole run. A state
                a reviewer can only hold is a state nobody can judge the motion
                of, and the motion is most of what the object is. */}
            <button
              type="button"
              className="arplay"
              aria-pressed={demo.playing}
              onClick={demo.toggle}
            >
              {demo.playing ? LABELS.runStop : LABELS.runPlay}
            </button>
            {states.map((name) => (
              <button
                type="button"
                key={name}
                className="archip"
                aria-pressed={name === state}
                onClick={() => setState(name)}
              >
                {name}
              </button>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

/** The air between the rail and the panel it opens. */
const PANEL_GAP = 8;

/**
 * How wide the panel is beside the rail, and the least air it leaves.
 *
 * Wide enough that a count tile holds its label on one line: "Duplicate pairs
 * open" wrapping under its own number is the difference between a figure with a
 * name and two stacked fragments.
 */
const PANEL_WIDTH = 408;
const PANEL_MARGIN = 12;

type PanelFrame = Readonly<{
  left: number;
  right?: number;
  bottom: number;
  width?: number;
  maxHeight: number;
}>;

/**
 * Where the panel goes, in viewport coordinates.
 *
 * It is FIXED and portalled to the body rather than positioned inside the block
 * it belongs to, and the reason is not preference: the rail scrolls its
 * destinations, so it carries `overflow-x: hidden`, and anything absolutely
 * positioned beside the rail is clipped by it.
 *
 * Beside the rail on a desktop, bottom-aligned to the block, so opening it does
 * not move the thing that opened it. On a phone the rail is the bottom bar and
 * there is no beside: the panel takes the width of the screen and sits above the
 * block instead.
 */
function usePanelFrame(
  anchor: RefObject<HTMLElement | null>,
  open: boolean,
  phone: boolean,
): PanelFrame | null {
  const [frame, setFrame] = useState<PanelFrame | null>(null);
  // Before paint, so the panel is never shown at the wrong place for one frame.
  useLayoutEffect(() => {
    if (!open) {
      setFrame(null);
      return;
    }
    const place = () => {
      const box = anchor.current?.getBoundingClientRect();
      if (!box) {
        return;
      }
      const height = globalThis.innerHeight;
      if (phone) {
        setFrame({
          left: PANEL_MARGIN,
          right: PANEL_MARGIN,
          bottom: height - box.top + PANEL_GAP,
          maxHeight: box.top - PANEL_GAP * 2,
        });
        return;
      }
      setFrame({
        left: box.right + PANEL_GAP,
        bottom: Math.max(PANEL_MARGIN, height - box.bottom),
        width: PANEL_WIDTH,
        maxHeight: height - PANEL_MARGIN * 2,
      });
    };
    place();
    // The anchor moves when the rail collapses, when the window changes size and
    // when anything it sits in scrolls, so the frame follows all three rather
    // than being measured once at open.
    globalThis.addEventListener("resize", place);
    globalThis.addEventListener("scroll", place, true);
    return () => {
      globalThis.removeEventListener("resize", place);
      globalThis.removeEventListener("scroll", place, true);
    };
  }, [anchor, open, phone]);
  return frame;
}

/**
 * What amber is about.
 *
 * Amber is the state a person is meant to notice and not act on this second, and
 * the licence is what puts the product there: it keeps working, and it keeps
 * saying so. The duplicate queue is the other thing that can hold amber, and it
 * ranks under the licence because it is true of one screen rather than of the
 * whole installation.
 */
function warningLine(signals: Signals): string {
  if (signals.license === "none" || signals.license === "refused") {
    return signals.licenseLine;
  }
  if (signals.duplicates) {
    return `${signals.duplicates} ${LABELS.duplicates}`;
  }
  return LABELS.review;
}

/**
 * The state the section shows when nobody has overridden it.
 *
 * The order is severity. Red is NOT CONNECTED and nothing else: a source the
 * agent cannot reach, or no model bound at all, in both cases the agent is not
 * running. Amber is the fault that can wait, and an unlicensed installation is
 * the case it exists for. Everything else is the tool working or at rest.
 */
function derive(
  activity: AppActivity,
  signals: Signals,
  server: AgentActivity,
): MarginceCoreState {
  if (signals.ai === "unconfigured" || signals.offline.length > 0) {
    return "error";
  }
  // It STAYS amber rather than escalating, because escalating would make the
  // chrome a sales surface.
  if (signals.license === "none" || signals.license === "refused") {
    return "warning";
  }
  // Below the two faults, because a source the agent cannot reach outranks work
  // in progress. Either side of the or is the same standing: the scheduled
  // runner works while nobody is looking at this tab, and a rail that moved only
  // for its own fetches reported an agent at rest whenever the reader was.
  if (server.working || activity.working) {
    return "working";
  }
  if (activity.reading) {
    return "ingest";
  }
  // A request that failed a moment ago does NOT colour the orb. One dropped
  // request on a flaky connection would otherwise flash the corner of every
  // screen red and green and red again, and a light that does that is a light
  // nobody reads. What the orb reports is standing state; the screen that made
  // the request reports the request.
  return "idle";
}

/**
 * The true things a resting agent can say, in the order it says them.
 *
 * Every one is a reading it already made. A kind with nothing to report is
 * absent rather than reworded into a cheerful nothing, so an installation with a
 * clean queue and no licence rotates through two lines and not five.
 */
function idleLines(
  signals: Signals,
  spend: Readonly<{ allowed: boolean; minor: number | undefined }>,
  money: string,
  devLine: string,
  /** The newest run that settled today, or null when there is none to name. */
  settledLine: string | null,
): readonly string[] {
  const said: Partial<Record<IdleKind, string>> = {
    // What the scheduled runner finished while nobody was looking. It rotates
    // rather than pinning the bar: `recent` is bounded to today, so a line
    // pinned to it would still be announcing this morning's brief at six in the
    // evening. In the rotation it is one true thing among the others.
    finished: settledLine ?? undefined,
    waiting:
      signals.waiting !== undefined && signals.waiting > 0
        ? `${signals.waiting} ${LABELS.waiting}`
        : undefined,
    duplicates:
      signals.duplicates !== undefined && signals.duplicates > 0
        ? `${signals.duplicates} ${LABELS.duplicatesIdle}`
        : undefined,
    spend:
      spend.allowed && spend.minor !== undefined
        ? `${money} ${LABELS.spentThisMonth}`
        : undefined,
    // The development path answers every call with an invention, and a reader who
    // does not know that is being misled by a product that looks like it works.
    model: signals.ai === "development" ? devLine : undefined,
    licence:
      signals.license === "none" || signals.license === "refused"
        ? signals.licenseLine
        : undefined,
  };
  const lines = IDLE_ORDER.map((kind) => said[kind]).filter(
    (line): line is string => line !== undefined,
  );
  return lines.length === 0 ? [LABELS.allClear] : lines;
}

/**
 * Which of those lines is showing, changing on its own.
 *
 * Slow: this sits at the edge of every screen all day, and a line that changed
 * every second would be movement in the corner of somebody's eye while they were
 * trying to read something else. One line, long enough to read twice.
 */
const IDLE_HOLD_MS = 5200;

function useIdleLine(lines: readonly string[]): string {
  const [at, setAt] = useState(0);
  const count = lines.length;
  useEffect(() => {
    if (count < 2) {
      return;
    }
    const timer = setInterval(
      () => setAt((current) => (current + 1) % count),
      IDLE_HOLD_MS,
    );
    return () => clearInterval(timer);
  }, [count]);
  // The list can shorten under it when a read answers, so the index is clamped
  // rather than trusted.
  return lines[at % count] ?? lines[0];
}

/** The one line the block carries, for whichever state is showing. */
function barLine(
  state: MarginceCoreState,
  signals: Signals,
  record: Readonly<{ reading: boolean }>,
  devLine: string,
  /** What the server says it is doing, or null when it says nothing this
   *  surface has words for. */
  serverLine: string | null,
): string {
  if (state === "error") {
    // Red says NOT CONNECTED, and the line says what is not connected: a
    // deployment with no model bound is a different repair from a source that
    // stopped answering, and "cannot reach Margince" would be wrong about both.
    if (signals.ai === "unconfigured") {
      return LABELS.noModel;
    }
    if (signals.offline.length > 0) {
      return `${LABELS.cannotReach} ${signals.offline.join(", ")}`;
    }
    return LABELS.unreachable;
  }
  if (state === "warning") {
    return warningLine(signals);
  }
  if (state === "working") {
    // The named run outranks the generic word: "Working" is true of both a local
    // save and an overnight brief, and only one of them is news.
    return serverLine ?? LABELS.working;
  }
  if (state === "ingest") {
    return record.reading ? LABELS.readingRecord : LABELS.reading;
  }
  if (signals.waiting !== undefined && signals.waiting > 0) {
    return `${signals.waiting} ${LABELS.waiting}`;
  }
  // A deployment on the development path is not disconnected — it answers — but
  // every answer it gives is invented, and a reader who does not know that is
  // being misled by a product that looks like it works. A standing fact, so the
  // resting line states it calmly rather than raising it as a fault, in the same
  // words the sign-in screen already uses for it.
  if (signals.ai === "development") {
    return devLine;
  }
  return LABELS.idle;
}

export function AgentRail({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  // The switcher's choice, and null while the rail is reporting what it read.
  const [override, setOverride] = useState<MarginceCoreState | null>(null);
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLElement>(null);
  const block = useRef<HTMLElement>(null);
  const phone = usePhoneViewport();
  const signals = useSignals();
  const model = useRecentCalls();
  const record = useRecordRead(route);
  const activity = useAppActivity();
  const server = useAgentActivity();
  const ticker = useAgentTicker();
  const spend = useAiSpend();
  const { locale } = useLocale();
  const demo = useDemoRun();
  // A run outranks everything, including a held state: it is the reviewer's own
  // request, and it lasts seconds.
  const state = demo.state ?? override ?? derive(activity, signals, server);

  // Put focus back on the block only when the panel actually HELD it: an outside
  // click usually lands on something focusable of its own, and pulling focus
  // back after it would undo what the click just did.
  const dismiss = useCallback(() => {
    const held = panel.current?.contains(document.activeElement) ?? false;
    setOpen(false);
    if (held) {
      trigger.current?.focus();
    }
  }, []);
  usePopoverDismiss(open, panel, dismiss);

  // Once per session, in the console: a build that can be put into a state no
  // installation is in has to say so where anyone inspecting the page will find
  // it. In an effect, because a render must not have side effects.
  useEffect(() => {
    if (uiPreviewAgentStatesEnabled()) {
      announceAgentStatesPreview();
    }
  }, []);

  const frame = usePanelFrame(block, open, phone);
  const money =
    spend.minor === undefined
      ? ""
      : formatMoney(spend.minor, spend.currency, locale);
  // Above the early return with every other hook: a screen this section draws
  // nothing on is still a render it has to make the same calls in.
  // The newest settled run, for the rotation; the newest live one, for the bar.
  // The bar keeps the live run because that is what is true this second.
  const settledLine = server.recent[0] ? lineFor(server.recent[0], t) : null;
  const resting = useIdleLine(
    idleLines(signals, spend, money, t("auth.coreDevelopment"), settledLine),
  );

  // The one screen it absents itself from, and the reason is not layout: the Ask
  // surface IS the agent, at hero size, and a second Core in the rail would be
  // the product disagreeing with itself about how many agents there are. The
  // railless screens need no rule of their own any more, because a section in
  // the rail is absent wherever the rail is. Below every hook, because a screen
  // this component draws nothing on is still a render it has to make the same
  // calls in.
  if (route.screen === "ai") {
    return null;
  }

  // Three things can hold the line, and this is their order. The switcher wins
  // because a reviewer asked for that state; then whatever the state itself has
  // to say, because a fault outranks small talk; and at rest, the rotation of
  // true readings.
  // The first run the server reports, when this surface has words for it. It
  // outranks the resting rotation: a live run is what is true right now, and the
  // rotation is what is true in general.
  const serverLine = server.running[0] ? lineFor(server.running[0], t) : null;
  const line =
    demo.said ??
    (override && REVIEW_ONLY[override]) ??
    (state === "idle"
      ? resting
      : barLine(state, signals, record, t("auth.coreDevelopment"), serverLine));
  return (
    <section
      className={RUNNING.has(state) ? "arblock working" : "arblock"}
      data-core-state={state}
      aria-label={LABELS.region}
      ref={block}
    >
      {/* Out of the rail and into the body: the rail clips what hangs beside it
          (usePanelFrame). The tone the panel is dressed in travels with it,
          because a portalled element inherits nothing from where it came from. */}
      {open &&
        frame &&
        createPortal(
          <div className="arloose" data-core-state={state}>
            <AgentPanel
              state={state}
              setState={setOverride}
              signals={signals}
              model={model}
              panel={panel}
              frame={frame}
              demo={demo}
              line={line}
              running={server.running}
              recent={server.recent}
              spend={spend}
            />
          </div>,
          document.body,
        )}
      {/* One button carries the whole block: the click, the accessible name and
          the expanded state. A wrapper with a click handler is a target no
          keyboard can reach, and the CTA underneath keeps its own. */}
      <button
        type="button"
        className="arhit"
        ref={trigger}
        aria-expanded={open}
        aria-label={open ? LABELS.collapse : LABELS.expand}
        onClick={() => setOpen((current) => !current)}
      >
        <MarginceCoreScene
          state={state}
          feed={false}
          size="md"
          className="arorb"
        />
        {/* Hidden by the stylesheet on the collapsed rail, where the orb and the
            count are the whole report and the button's name carries the rest. */}
        <span className="arwords">
          {/* One named thing at a time while the tool is fetching, the state's
              own line when it is not. Keyed on the event id so a repeated phrase
              still arrives as a new line rather than sitting there looking
              stuck: what a reader is counting is EVENTS, and two identical
              sentences in a row are two of them. */}
          {ticker.length > 0 && demo.said === null ? (
            <span className="arline arsaid" key={ticker[0].id}>
              {ticker[0].said}
            </span>
          ) : (
            <span className="arline">{line}</span>
          )}
          {/* The spend sits in the bar and not only in the panel: it is the one
              figure somebody is accountable for, and a number nobody opens a
              panel to see is a number nobody sees. Absent when this seat may not
              read it, and absent again when nothing in the month carried a
              price. */}
          {spend.allowed && spend.minor !== undefined && (
            <span className="arspend">{money}</span>
          )}
        </span>
        <ChevronRight
          size={15}
          className={open ? "archev open" : "archev"}
          aria-hidden="true"
        />
      </button>
    </section>
  );
}
