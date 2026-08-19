import {
  ChevronDown,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
} from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { Button, Textarea } from "../design-system/atoms";
import { MarginceCoreScene } from "../design-system/margince-core";
import { useT } from "../i18n";
import { useAgentTierMap } from "./autonomy";
import { useRouteSubject } from "./pagemeta";
import { usePopoverDismiss } from "./popover";
import type { Route } from "./router";
import "./agentdock.css";

// The agent dock: the Margince Core floating at the foot of the content column,
// and everything the runtime can honestly say about the agent behind it.
//
// It is the ONLY floating AI affordance in the product. It used to share the
// page with a separate "Ask about this" FAB in the opposite corner, and two
// glowing corners left a reader unable to tell which of them was the agent — so
// the FAB's composer moved in here (B-EP09.6, AC-shell-8) and the FAB is gone.
//
// Three densities, because the agent is present on every screen and only
// sometimes the thing you came for:
//   at rest   — who it is and what state it is in, and nothing else;
//   on hover  — the affordance that says there is more here;
//   on click  — the panel, where the ask composer and the numbers live.
// What wants attention is exempt from that ladder: a waiting-approvals count is
// shown at rest, because a signal you have to hover to find is not a signal.
//
// What it may CLAIM is constrained. The runtime knows routing is *configured*;
// it does not continuously prove a provider is reachable, so the dock states
// configuration and never liveness. The status dot is the neutral tone for the
// same reason — a success green beside "Configured" would read as a health
// check nobody performed. The composer's scope line is load-bearing for the
// same reason: the agent reads only the RBAC ∩ Passport intersection, and the
// panel must never imply more.
//
// The orb is the Core primitive (WDS-CORE-1), the same ball sign-in and
// onboarding show — there is one orb in the product, and a CSS lookalike in
// permanent chrome would be a second. `dormant` is the honest state for a
// surface claiming configuration rather than work in flight — the waiting count
// beside it is what carries the one live signal this dock has, in a number a
// reader can act on. The feed is off: nothing is arriving here, and a mote
// drifting over the page is a moving speck at the foot of the column.

// The tools the catalog actually carries, split by how they act. Absent a
// loaded catalog the row that reads this is not rendered — "0 auto-execute" is
// a claim about the installation, and a pending request is not one.
function tierCounts(map: Record<string, string>): {
  auto: number;
  confirm: number;
  total: number;
} {
  const tiers = Object.values(map);
  const auto = tiers.filter((tier) => tier === "auto_execute").length;
  return { auto, confirm: tiers.length - auto, total: tiers.length };
}

// The record-scoped ask, inherited whole from the FAB this dock absorbed: the
// same copy under the same keys, in the same three parts. The context line
// names what the question will be asked ABOUT, in the same words the trail at
// the top of the window uses (app/pagemeta.ts) — and the scope line under it is
// the caveat that keeps the naming honest.
function AskComposer({ route }: Readonly<{ route: Route }>) {
  const t = useT();
  const context = useRouteSubject(route);
  return (
    // Named, so a screen reader landing inside the panel knows which of its
    // parts it is standing in: the composer is the one part of this panel that
    // takes input rather than pointing somewhere else.
    <section className="agentask" aria-label={t("fab.panelAria")}>
      <span className="t-label">{t("fab.context", { context })}</span>
      <p className="t-caption agentaskscope">{t("fab.scope")}</p>
      <Textarea
        aria-label={t("fab.inputAria")}
        placeholder={t("fab.placeholder")}
        rows={3}
      />
      <Button variant="primary" small>
        {t("fab.send")}
      </Button>
    </section>
  );
}

function DockPanel({
  route,
  waiting,
  panel,
}: Readonly<{
  route: Route;
  waiting: number | undefined;
  panel: React.RefObject<HTMLElement | null>;
}>) {
  const t = useT();
  const tools = tierCounts(useAgentTierMap());
  return (
    // A section, not a div: `aria-label` names an element only where the role
    // supports naming, so on a bare div the name is dropped and the panel is
    // unreachable by name. Named, it is a region a screen reader can land on.
    //
    // A region and NOT role="dialog", which is what the FAB's panel claimed:
    // this is a popover. Nothing traps focus in it, the page behind it stays
    // live and clickable, and an outside click dismisses it — the dialog role
    // would promise a modal contract that none of that keeps.
    //
    // The order below is deliberate, narrowest first: who you are talking to,
    // then the question scoped to the record you are standing on, then the full
    // Ask surface for anything wider, then what the agent has waiting and what
    // it is allowed to do, and last the block that is only example data. The
    // composer leads because it is what a reader opening this dock from a record
    // came to do, and "Ask Margince" only reads as "wider than this one record"
    // once the narrower thing is visible above it.
    <section
      className="agentpanel"
      ref={panel}
      aria-label={t("agent.regionAria")}
    >
      <div className="agentpanelhead">
        <MarginceCoreScene
          state="dormant"
          feed={false}
          className="agentorb big"
        />
        <span className="agentwho">
          <b>{t("agent.title")}</b>
          <span className="agentstate">{t("agent.configured")}</span>
        </span>
      </div>
      {/* Everywhere except the full Ask surface: there, a scoped composer would
          be a smaller copy of the page behind it, offering to ask about the
          screen the reader is already asking on. */}
      {route.screen !== "ai" && <AskComposer route={route} />}
      <a className="agentcta" href="#/ai">
        <Sparkles size={15} aria-hidden />
        {t("nav.ai")}
      </a>
      <div className="agentrows">
        {/* Both rows read live state, so both are absent while there is none to
            read rather than standing in with a zero. */}
        {waiting !== undefined && (
          <a className="agentrow" href="#/inbox">
            <ShieldCheck size={15} aria-hidden />
            <span>{t("agent.approvals")}</span>
            <b className={waiting > 0 ? "agentvalue due" : "agentvalue"}>
              {waiting}
            </b>
          </a>
        )}
        {tools.total > 0 && (
          <a className="agentrow" href="#/settings/agents">
            <SlidersHorizontal size={15} aria-hidden />
            <span>{t("agent.toolsTitle")}</span>
            <span className="agentvalue">
              {t("agent.toolsSummary", {
                auto: String(tools.auto),
                confirm: String(tools.confirm),
              })}
            </span>
          </a>
        )}
      </div>
      {/* Everything below the marker is example data: the AI activity list has
          no handler behind it, and routing and spend are not wired into the
          shell. The marker leads the block rather than trailing it, so the
          values are labelled before they are read. */}
      <div className="agentexample">
        <p className="agentfixture t-mono">{t("agent.fixture")}</p>
        <dl>
          <div>
            <dt>{t("agent.activityLabel")}</dt>
            <dd>{t("agent.exampleActivity")}</dd>
          </div>
          <div>
            <dt>{t("agent.routingLabel")}</dt>
            <dd>{t("agent.exampleRouting")}</dd>
          </div>
          <div>
            <dt>{t("agent.spendLabel")}</dt>
            <dd>{t("agent.exampleCost")}</dd>
          </div>
        </dl>
      </div>
    </section>
  );
}

export function AgentDock({
  route,
  approvalsWaiting,
}: Readonly<{ route: Route; approvalsWaiting?: number }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLElement>(null);

  // Close, and put focus back where it can be used — but only when the panel
  // actually HELD focus. An outside click usually lands on something focusable
  // of its own, and pulling focus onto the trigger after it would undo what the
  // click just did.
  const dismiss = useCallback(() => {
    const held = panel.current?.contains(document.activeElement) ?? false;
    setOpen(false);
    if (held) {
      trigger.current?.focus();
    }
  }, []);
  usePopoverDismiss(open, panel, dismiss);

  const waiting = approvalsWaiting;
  return (
    <div className={open ? "agentdock open" : "agentdock"}>
      <button
        type="button"
        className="agentdocktrigger"
        ref={trigger}
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        <MarginceCoreScene state="dormant" feed={false} className="agentorb" />
        <span className="agentwho">
          <b>{t("agent.title")}</b>
          <span className="agentstate">{t("agent.configured")}</span>
        </span>
        {/* The count is part of the trigger's name on purpose: a badge that only
            a sighted user can count is half a signal. */}
        {waiting !== undefined && waiting > 0 && (
          <span className="agentwait">
            {waiting}
            <span className="sr-only"> {t("agent.approvals")}</span>
          </span>
        )}
        <ChevronDown size={15} className="agentchev" aria-hidden />
      </button>
      {/* After the trigger in the DOM even though it opens ABOVE it: this is a
          disclosure, and a Tab out of the trigger has to land in what the
          trigger just expanded. CSS does the upward placement. */}
      {open && <DockPanel route={route} waiting={waiting} panel={panel} />}
    </div>
  );
}
