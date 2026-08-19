// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { ChevronUp } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { INTL_LOCALE } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useOrganization360 } from "../screens/company360";
import { useConnectors } from "../screens/connectors";
import { useDedupeQueue } from "../screens/dedupe";
import { useEntityName } from "../screens/entityref";
import { usePendingApprovals } from "../screens/inbox.queries";
import { type AppActivity, useAppActivity } from "./activity";
import { RUNNING } from "./agenttaskbar-copy";
import { useAttention } from "./attention";
import { useAgentTierMap } from "./autonomy";
import { useCan } from "./capability";
import { SCREEN_ENTITY } from "./entity";
import { NAV, RAIL_LESS_SCREENS } from "./nav";
import { usePopoverDismiss } from "./popover";
import type { Route } from "./router";
import "./agenttaskbar.css";

// The agent taskbar — the shipped agent surface, drawn on every screen.
//
// One bar docked at the bottom of the viewport, carrying the Core as its
// status light. Left of the bar is the page you are standing on; right of it
// is everything else the agent is doing. An agent that reports only global
// work is a background job with a light on it, and one that reports only the
// current record is a sidebar — omnipresence means both at once, in one line,
// at the edge of the screen you are already looking at.
//
// EVERYTHING THE BAR REPORTS IS READ FROM THE API: approvals waiting, which
// sources are unreachable, the model the last call actually ran on, this
// account's own suggestions. Nothing is a zero standing in for a read that has
// not answered — a row whose read is pending, or that this seat may not make, is
// absent instead. The bar reaches every state it shows (dormant, ingesting,
// reasoning, disconnected, error) on its own, from what it read; nothing here
// is invented.

type Suggestion = components["schemas"]["Organization360Suggestion"];

/** How many of the agent's last actions the panel recaps. */
const RECAP_ROWS = 5;

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
}>;

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
 * the two to report calmly. Everything else rests at `dormant` — proposals
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
  const approvals = usePendingApprovals();
  const connectors = useConnectors();
  const dedupe = useDedupeQueue();
  const ai = useAiPosture();

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
  };
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
    queryKey: ["ai-calls", "taskbar-recent"],
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
 * What the runtime row prints, in the three cases it genuinely has: the model
 * the last call was SERVED by — not the configured one, because a fallback
 * ladder makes those differ exactly when it matters — or the reason there is
 * none.
 */
function modelText(
  t: ReturnType<typeof useT>,
  read: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>,
): string {
  const latest = read.calls[0];
  if (latest) {
    return `${latest.provider}/${latest.served_model}`;
  }
  return read.allowed ? t("taskbar.noCallsYet") : t("taskbar.unreadable");
}

/**
 * The page half of the bar's awareness: the route, and the record's real name.
 *
 * The nav label alone on a list screen, the name beside it on a record — the same
 * resolution the page-head trail does, so the two can never name one place two
 * different things.
 */
function ScopeChip({
  route,
  scope,
}: Readonly<{ route: Route; scope: string | undefined }>) {
  const t = useT();
  const kind = route.id ? SCREEN_ENTITY[route.screen] : undefined;
  // Hooks cannot be conditional, so the name is always asked for; without a kind
  // the id is withheld and the query stays disabled, which is the case a list
  // screen is in anyway.
  const name = useEntityName(kind ?? "person", kind ? route.id : undefined);
  const navItem = NAV.find((item) => item.screen === route.screen);
  const where = navItem ? t(navItem.labelKey) : route.screen;
  // What the SCREEN says it is about wins over what the route says: a screen
  // reporting three rows selected knows something the URL does not.
  const what = scope ?? name;
  return (
    <span className="tbscope t-mono">
      {what ? `${where} · ${what}` : where}
    </span>
  );
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
function useRecordRead(route: Route): Readonly<{
  reading: boolean;
  suggestions: readonly Suggestion[];
}> {
  const isCompany = route.screen === "companies" && Boolean(route.id);
  // Enabled, not id-juggled: an empty id is a request the server answers 422,
  // and chrome that fires one on every screen is chrome that logs an error on
  // every screen.
  const org = useOrganization360(route.id ?? "", isCompany);
  const view = org.data?.state === "ready" ? org.data.view : undefined;
  return {
    reading: isCompany && org.isFetching,
    suggestions: view?.suggestions ?? [],
  };
}

function PageFindings({
  suggestions,
}: Readonly<{ suggestions: readonly Suggestion[] }>) {
  const t = useT();
  if (suggestions.length === 0) {
    return <p className="tbitem tbempty">{t("taskbar.nothingHere")}</p>;
  }
  return (
    <>
      {suggestions.map((suggestion) => (
        <p className="tbitem" key={suggestion.fingerprint}>
          <span className="tbmark" aria-hidden="true" />
          {suggestion.title ?? suggestion.reason}
          <span className="tbmuted t-mono">{suggestion.kind}</span>
        </p>
      ))}
    </>
  );
}

/**
 * The recap: what the agent has done lately, and the door to the whole trace.
 *
 * Five rows at most. The question a person asks of a background agent is "what
 * have you been doing", and five answers it — a sixth turns the panel into a log
 * viewer, which already exists and is better at it.
 */
/** The task in the reader's words, or the token opened up if it is a new one. */
function saidFor(t: ReturnType<typeof useT>, task: string): string {
  // The lookup key is namespaced (`taskbar.task.<token>`), so a task named
  // `constructor` cannot land on `Object.prototype`'s own member the way a
  // bare `TASK_SAID[task]` lookup once could — `translate` falls back to
  // returning the key itself when the catalog has no entry for it.
  const key = `taskbar.task.${task}` as MessageKey;
  const said = t(key);
  return said === key ? task.replaceAll("_", " ") : said;
}

/**
 * When it happened, as a person would say it.
 *
 * A wall-clock stamp answers "at what time", and the question a recap answers is
 * "how long ago" — five rows of `19/08/2026, 10:00` make the reader do the
 * subtraction, five times, to learn that everything happened this morning.
 */
function agoFor(
  t: ReturnType<typeof useT>,
  iso: string,
  locale: Locale,
  now: number,
): string {
  const seconds = Math.round((Date.parse(iso) - now) / 1000);
  if (Number.isNaN(seconds) || seconds > -60) {
    return t("taskbar.justNow");
  }
  const format = new Intl.RelativeTimeFormat(INTL_LOCALE[locale], {
    numeric: "auto",
  });
  const [unit, size]: [Intl.RelativeTimeFormatUnit, number] =
    seconds > -3600
      ? ["minute", 60]
      : seconds > -86_400
        ? ["hour", 3600]
        : ["day", 86_400];
  return format.format(Math.round(seconds / size), unit);
}

function Recap({
  recent,
}: Readonly<{
  recent: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
}>) {
  const t = useT();
  const { locale } = useLocale();
  // Read once per open, so five rows share one reading of the clock and cannot
  // disagree about what "now" is.
  const now = Date.now();
  if (!recent.allowed) {
    return <p className="tbitem tbempty">{t("taskbar.logUnreadable")}</p>;
  }
  if (recent.calls.length === 0) {
    return <p className="tbitem tbempty">{t("taskbar.noCallsYet")}</p>;
  }
  return (
    <>
      {recent.calls.map((call) => (
        <p className="tbitem" key={call.id}>
          <span className="tbmark" aria-hidden="true" />
          {saidFor(t, call.task)}
          <span className="tbmuted">
            {agoFor(t, call.occurred_at, locale, now)}
          </span>
        </p>
      ))}
      <a className="tbmore" href={AI_SETTINGS_HREF}>
        {t("taskbar.fullLog")}
      </a>
    </>
  );
}

function RuntimeRows({
  offline,
  model,
  ai,
}: Readonly<{
  offline: readonly string[];
  model: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
  ai: AiPosture;
}>) {
  const t = useT();
  const tools = Object.values(useAgentTierMap()).length;
  return (
    <div className="tbmeta">
      {/* The posture leads, because it decides whether anything below it means
          anything: a model name from last week is not a model bound today. */}
      {ai === "unconfigured" && (
        <span className="tbwarn">{t("taskbar.noModel")}</span>
      )}
      {ai === "development" && (
        <span>
          <b>{t("auth.coreDevelopment")}</b> {t("auth.coreModeDevelopment")}
        </span>
      )}
      <span>
        {t("taskbar.model")}{" "}
        {model.calls.length > 0 ? (
          <b>{modelText(t, model)}</b>
        ) : (
          <i>{modelText(t, model)}</i>
        )}
      </span>
      {tools > 0 && (
        <span>
          {t("taskbar.tools")} <b>{tools}</b>
        </span>
      )}
      {offline.map((source) => (
        <span className="tbconn down" key={source}>
          <i aria-hidden="true" />
          {`${source} ${t("taskbar.offline")}`}
        </span>
      ))}
    </div>
  );
}

function TaskbarPanel({
  suggestions,
  signals,
  model,
  panel,
}: Readonly<{
  suggestions: readonly Suggestion[];
  signals: Signals;
  model: Readonly<{ allowed: boolean; calls: readonly AiCall[] }>;
  panel: React.RefObject<HTMLElement | null>;
}>) {
  const t = useT();
  return (
    <section className="tbpanel" ref={panel} aria-label={t("taskbar.region")}>
      <div className="tbsect">
        <h4>{t("taskbar.onThisPage")}</h4>
        <PageFindings suggestions={suggestions} />
      </div>
      <div className="tbsect">
        <h4>{t("taskbar.acrossWorkspace")}</h4>
        {/* Absent, not zero, while the read has not answered: a count nobody
            computed is the one thing a status surface must not print. */}
        {signals.waiting !== undefined && (
          <a className="tbitem tblink" href="#/inbox">
            <span className="tbmark" aria-hidden="true" />
            {t("taskbar.approvals")}
            <span className="tbmuted">{signals.waiting}</span>
          </a>
        )}
        {signals.duplicates !== undefined && (
          <a className="tbitem tblink" href="#/dedupe">
            <span className="tbmark" aria-hidden="true" />
            {t("taskbar.duplicatesRow")}
            <span className="tbmuted">{signals.duplicates}</span>
          </a>
        )}
      </div>
      <div className="tbsect">
        <h4>{t("taskbar.recap")}</h4>
        <Recap recent={model} />
      </div>
      <div className="tbsect">
        <h4>{t("taskbar.runtime")}</h4>
        <RuntimeRows offline={signals.offline} model={model} ai={signals.ai} />
      </div>
    </section>
  );
}

/**
 * The state the bar shows, derived from what it read.
 *
 * At rest, taking something in, working on something, cut off, or stopped —
 * that is the whole vocabulary a reader has to learn for a thing that sits on
 * every screen all day, and it is small on purpose. An earlier build lit a
 * beat for each write that landed, and what a reader actually experienced was
 * a check mark flashing at random while they clicked around, which teaches
 * them to stop looking at it.
 *
 * The order is severity, then immediacy. A failure outranks everything because
 * it is the only one the reader may have to act on; a source the agent cannot
 * reach outranks work in flight, because work done against half the evidence is
 * the more dangerous thing to report calmly.
 */
function derive(activity: AppActivity, signals: Signals): MarginceCoreState {
  if (activity.failed) {
    return "error";
  }
  // No model bound outranks everything but a dead server: an agent that cannot
  // think is not idle, and reporting it as idle is the most misleading thing
  // this bar could do.
  if (signals.ai === "unconfigured" || signals.offline.length > 0) {
    return "disconnected";
  }
  if (activity.working) {
    return "reasoning";
  }
  if (activity.reading) {
    return "ingesting";
  }
  return "dormant";
}

/** The one line the collapsed bar carries, for whichever state is showing. */
function barLine(
  t: ReturnType<typeof useT>,
  state: MarginceCoreState,
  signals: Signals,
  record: Readonly<{ reading: boolean }>,
  devLine: string,
): string {
  if (state === "ingesting") {
    return record.reading ? t("taskbar.readingRecord") : t("taskbar.reading");
  }
  if (state === "reasoning") {
    return t("taskbar.working");
  }
  if (state === "error") {
    return t("taskbar.unreachable");
  }
  if (state === "disconnected") {
    return signals.ai === "unconfigured"
      ? t("taskbar.noModel")
      : `${t("taskbar.cannotReach")} ${signals.offline.join(", ")}`;
  }
  if (signals.waiting !== undefined && signals.waiting > 0) {
    return `${signals.waiting} ${t("taskbar.waiting")}`;
  }
  // A deployment on the development path is not disconnected — it answers — but
  // every answer it gives is invented, and a reader who does not know that is
  // being misled by a product that looks like it works. A standing fact, so the
  // resting line states it calmly rather than raising it as a fault, in the same
  // words the sign-in screen already uses for it.
  if (signals.ai === "development") {
    return devLine;
  }
  return t("taskbar.idle");
}

/** Where the bar sends you, when there is somewhere to send you. */
function barCta(
  t: ReturnType<typeof useT>,
  state: MarginceCoreState,
  signals: Signals,
): Readonly<{ label: string; href: string }> | null {
  if (state === "disconnected") {
    return signals.ai === "unconfigured"
      ? { label: t("taskbar.configure"), href: AI_SETTINGS_HREF }
      : { label: t("taskbar.reconnect"), href: "#/settings/connections" };
  }
  if (signals.waiting !== undefined && signals.waiting > 0) {
    return { label: t("taskbar.review"), href: "#/inbox" };
  }
  return null;
}

export function AgentTaskbar({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLElement>(null);
  const signals = useSignals();
  const model = useRecentCalls();
  const record = useRecordRead(route);
  const activity = useAppActivity();
  const scope = useAttention();
  const state = derive(activity, signals);

  // Put focus back on the bar only when the panel actually HELD it: an outside
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

  // The same screens the Ask FAB absents itself from, for the same reasons: the
  // full Ask surface already IS the agent, and a railless screen — onboarding, a
  // booking page, a client surface — has no app chrome for a bar to belong to.
  // Onboarding is the sharpest case, because it shows the Core at hero size and
  // a second orb in the corner would be the product disagreeing with itself
  // about how many agents there are.
  if (route.screen === "ai" || RAIL_LESS_SCREENS.has(route.screen)) {
    return null;
  }

  const cta = barCta(t, state, signals);
  return (
    <div className={open ? "tbdock open" : "tbdock"} data-core-state={state}>
      {open && (
        <TaskbarPanel
          suggestions={record.suggestions}
          signals={signals}
          model={model}
          panel={panel}
        />
      )}
      <div className={RUNNING.has(state) ? "tbbar working" : "tbbar"}>
        {/* The whole bar expands, and this is how it stays valid markup: one
            full-bleed button under the content carries the click, the accessible
            name and the expanded state, and the link above it keeps its own. A
            wrapper with a click handler is a target no keyboard can reach. */}
        <button
          type="button"
          className="tbhit"
          ref={trigger}
          aria-expanded={open}
          aria-label={open ? t("taskbar.collapse") : t("taskbar.expand")}
          onClick={() => setOpen((current) => !current)}
        />
        <MarginceCoreScene state={state} feed={false} className="tborb" />
        <ScopeChip route={route} scope={scope?.label} />
        <span className="tbline">
          {barLine(t, state, signals, record, t("auth.coreDevelopment"))}
        </span>
        {cta ? (
          <a className="tbgo" href={cta.href}>
            {cta.label}
          </a>
        ) : (
          signals.waiting !== undefined && (
            <span className="tbbadge">
              <span className="tbpip" aria-hidden="true" />
              <b>{signals.waiting}</b>
            </span>
          )
        )}
        <ChevronUp size={15} className="tbchev" aria-hidden="true" />
      </div>
    </div>
  );
}
