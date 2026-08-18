// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { ChevronUp } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useT } from "../i18n";
import { useOrganization360 } from "../screens/company360";
import { useConnectors } from "../screens/connectors";
import { useDedupeQueue } from "../screens/dedupe";
import { useEntityName } from "../screens/entityref";
import { usePendingApprovals } from "../screens/inbox.queries";
import { LABELS, REVIEW_ONLY, RUNNING, VOCABULARY } from "./agenttaskbar-copy";
import { useAgentTierMap } from "./autonomy";
import { useCan } from "./capability";
import { SCREEN_ENTITY } from "./entity";
import { NAV, RAIL_LESS_SCREENS } from "./nav";
import { usePopoverDismiss } from "./popover";
import type { Route } from "./router";
import { announceTaskbarPreview } from "./ui-preview";
import "./agenttaskbar.css";

// The agent taskbar — a UI-PREVIEW SURFACE, not a feature (app/ui-preview.ts).
//
// The shipped agent surface is the dock beside the page title
// (app/agentdock.tsx). This is the competing proposal for the same job: one bar
// docked at the bottom of the viewport, present on every screen, carrying the
// Core as its status light.
//
// The idea being judged is the SPLIT, which a page-head dock cannot do. Left of
// the bar is the page you are standing on; right of it is everything else the
// agent is doing. An agent that reports only global work is a background job
// with a light on it, and one that reports only the current record is a sidebar
// — the claim here is that omnipresence means both at once, in one line, at the
// edge of the screen you are already looking at.
//
// EVERYTHING THE BAR REPORTS IS READ FROM THE API: approvals waiting, which
// sources are unreachable, the model the last call actually ran on, this
// account's own suggestions. Nothing is a zero standing in for a read that has
// not answered — a row whose read is pending, or that this seat may not make, is
// absent instead. The one invented table left is `REVIEW_ONLY`
// (agenttaskbar-copy.ts): the states no read can reach, offered from the
// switcher in the panel under a heading that says review-only. The bar never
// enters one of those on its own.

/** What the installation can actually tell us, and what it cannot. */
type Signals = Readonly<{
  /** The state the reads add up to. */
  state: MarginceCoreState;
  /** Approvals staged for this human; undefined until the read answers. */
  waiting: number | undefined;
  /** Sources the agent cannot reach, named as the reader knows them. */
  offline: readonly string[];
  /** Duplicate pairs the agent will not decide for itself; undefined until read. */
  duplicates: number | undefined;
}>;

/**
 * The reads the bar's right half stands on, and the state they add up to.
 *
 * Order is severity, not convenience: a source the agent cannot reach outranks a
 * queue, because a queue built from half the evidence is the more dangerous of
 * the two to report calmly. Everything else rests at `dormant` — proposals
 * WAITING is not a state of its own, it is the agent at rest with a number
 * beside it, and that number is the thing a person acts on.
 */
function useSignals(): Signals {
  const approvals = usePendingApprovals();
  const connectors = useConnectors();
  const dedupe = useDedupeQueue();

  const offline = (connectors.data?.data ?? [])
    .filter((connection) => connection.status !== "connected")
    .map((connection) => connection.account_label ?? connection.provider);
  const duplicates = dedupe.data ? dedupe.data.data.length : undefined;

  return {
    state:
      offline.length > 0 ? "disconnected" : duplicates ? "flagged" : "dormant",
    // Absent `data` means the read has not answered, or was refused. A 0 here
    // would be this surface inventing an all-clear.
    waiting: approvals.data ? approvals.data.data.length : undefined,
    offline,
    duplicates,
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
type ModelRead =
  /** The seat may not ask. */
  | { kind: "forbidden" }
  /** It may, and the installation has not run a call yet. */
  | { kind: "none" }
  | { kind: "model"; label: string };

function useServedModel(): ModelRead {
  const allowed = useCan("automation", "update");
  const latest = useQuery({
    queryKey: ["ai-calls", "taskbar-latest"],
    enabled: allowed,
    staleTime: 60_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/calls", {
        params: { query: { limit: 1 } },
      });
      if (error) {
        // Chrome must not take a page down over telemetry: an unavailable model
        // is a state this surface draws, not an error it throws.
        return null;
      }
      return data.data[0] ?? null;
    },
  });
  if (!allowed) {
    return { kind: "forbidden" };
  }
  const call = latest.data;
  // Pending reads as "nothing yet" for one tick rather than flashing a model in:
  // the row is one line and a value that appears late is easier to read than a
  // value that changes.
  return call
    ? { kind: "model", label: `${call.provider}/${call.served_model}` }
    : { kind: "none" };
}

/** What the runtime row prints for each of the three outcomes. */
function modelText(read: ModelRead): string {
  if (read.kind === "model") {
    return read.label;
  }
  return read.kind === "forbidden" ? LABELS.unreadable : LABELS.noCallsYet;
}

/**
 * The page half of the bar's awareness: the route, and the record's real name.
 *
 * The nav label alone on a list screen, the name beside it on a record — the same
 * resolution the page-head trail does, so the two can never name one place two
 * different things.
 */
function ScopeChip({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  const kind = route.id ? SCREEN_ENTITY[route.screen] : undefined;
  // Hooks cannot be conditional, so the name is always asked for; without a kind
  // the id is withheld and the query stays disabled, which is the case a list
  // screen is in anyway.
  const name = useEntityName(kind ?? "person", kind ? route.id : undefined);
  const navItem = NAV.find((item) => item.screen === route.screen);
  const where = navItem ? t(navItem.labelKey) : route.screen;
  return (
    <span className="tbscope t-mono">
      {name ? `${where} · ${name}` : where}
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
function PageFindings({ route }: Readonly<{ route: Route }>) {
  const isCompany = route.screen === "companies" && Boolean(route.id);
  const org = useOrganization360(isCompany ? (route.id ?? "") : "");
  const view = org.data?.state === "ready" ? org.data.view : undefined;
  const suggestions = view?.suggestions ?? [];
  if (suggestions.length === 0) {
    return <p className="tbitem tbempty">{LABELS.nothingHere}</p>;
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

function RuntimeRows({
  offline,
  model,
}: Readonly<{ offline: readonly string[]; model: ModelRead }>) {
  const tools = Object.values(useAgentTierMap()).length;
  return (
    <div className="tbmeta">
      <span>
        {LABELS.model}{" "}
        {model.kind === "model" ? (
          <b>{modelText(model)}</b>
        ) : (
          <i>{modelText(model)}</i>
        )}
      </span>
      {tools > 0 && (
        <span>
          {LABELS.tools} <b>{tools}</b>
        </span>
      )}
      {offline.map((source) => (
        <span className="tbconn down" key={source}>
          <i aria-hidden="true" />
          {`${source} ${LABELS.offline}`}
        </span>
      ))}
    </div>
  );
}

function TaskbarPanel({
  state,
  setState,
  route,
  signals,
  model,
  panel,
}: Readonly<{
  state: MarginceCoreState;
  setState: (next: MarginceCoreState) => void;
  route: Route;
  signals: Signals;
  model: ModelRead;
  panel: React.RefObject<HTMLElement | null>;
}>) {
  const states = VOCABULARY;
  return (
    <section className="tbpanel" ref={panel} aria-label={LABELS.region}>
      <div className="tbsect">
        <h4>{LABELS.onThisPage}</h4>
        <PageFindings route={route} />
      </div>
      <div className="tbsect">
        <h4>{LABELS.acrossWorkspace}</h4>
        {/* Absent, not zero, while the read has not answered: a count nobody
            computed is the one thing a status surface must not print. */}
        {signals.waiting !== undefined && (
          <a className="tbitem tblink" href="#/inbox">
            <span className="tbmark" aria-hidden="true" />
            {LABELS.approvals}
            <span className="tbmuted">{signals.waiting}</span>
          </a>
        )}
        {signals.duplicates !== undefined && (
          <a className="tbitem tblink" href="#/dedupe">
            <span className="tbmark" aria-hidden="true" />
            {LABELS.duplicatesRow}
            <span className="tbmuted">{signals.duplicates}</span>
          </a>
        )}
      </div>
      <div className="tbsect">
        <h4>{LABELS.runtime}</h4>
        <RuntimeRows offline={signals.offline} model={model} />
      </div>
      <div className="tbsect tbstates">
        <h4>{LABELS.states}</h4>
        <div className="tbchips">
          {states.map((name) => (
            <button
              type="button"
              key={name}
              className="tbchip"
              aria-pressed={name === state}
              onClick={() => setState(name)}
            >
              {name}
            </button>
          ))}
        </div>
      </div>
    </section>
  );
}

/** The one line the collapsed bar carries, for whichever state is showing. */
function barLine(state: MarginceCoreState, signals: Signals): string {
  const invented = REVIEW_ONLY[state];
  if (invented) {
    return invented;
  }
  if (state === "disconnected") {
    return `${LABELS.cannotReach} ${signals.offline.join(", ")}`;
  }
  if (state === "flagged" && signals.duplicates) {
    return `${signals.duplicates} ${LABELS.duplicates}`;
  }
  if (signals.waiting !== undefined && signals.waiting > 0) {
    return `${signals.waiting} ${LABELS.waiting}`;
  }
  return LABELS.idle;
}

/** Where the bar sends you, when there is somewhere to send you. */
function barCta(
  state: MarginceCoreState,
  signals: Signals,
): Readonly<{ label: string; href: string }> | null {
  if (state === "disconnected") {
    return { label: LABELS.reconnect, href: "#/settings/connections" };
  }
  if (state === "flagged" && signals.duplicates) {
    return { label: LABELS.decide, href: "#/dedupe" };
  }
  if (signals.waiting !== undefined && signals.waiting > 0) {
    return { label: LABELS.review, href: "#/inbox" };
  }
  return null;
}

export function AgentTaskbar({ route }: Readonly<{ route: Route }>) {
  // The switcher's choice, and null while the bar is reporting what it read.
  const [override, setOverride] = useState<MarginceCoreState | null>(null);
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLElement>(null);
  const signals = useSignals();
  const model = useServedModel();
  const state = override ?? signals.state;

  // Once per session, in the console: a preview surface has to say so where
  // anyone inspecting the page will find it. In an effect, because a render must
  // not have side effects.
  useEffect(announceTaskbarPreview, []);

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

  const cta = barCta(state, signals);
  return (
    <div className={open ? "tbdock open" : "tbdock"} data-core-state={state}>
      {open && (
        <TaskbarPanel
          state={state}
          setState={setOverride}
          route={route}
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
          aria-label={open ? LABELS.collapse : LABELS.expand}
          onClick={() => setOpen((current) => !current)}
        />
        <MarginceCoreScene state={state} feed={false} className="tborb" />
        <ScopeChip route={route} />
        <span className="tbline">{barLine(state, signals)}</span>
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
