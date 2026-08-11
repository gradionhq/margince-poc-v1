import {
  ChevronDown,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
} from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { MarginceCoreScene } from "../design-system/margince-core";
import { useT } from "../i18n";
import { useAgentTierMap } from "./autonomy";
import { usePopoverDismiss } from "./popover";
import "./agentdock.css";

// The agent dock: the Margince Core at the right edge of the page head, and
// everything the runtime can honestly say about the agent behind it.
//
// Three densities, because the agent is present on every screen and only
// sometimes the thing you came for:
//   at rest   — who it is and what state it is in, and nothing else;
//   on hover  — the affordance that says there is more here;
//   on click  — the panel, where the numbers live.
// What wants attention is exempt from that ladder: a waiting-approvals count is
// shown at rest, because a signal you have to hover to find is not a signal.
//
// What it may CLAIM is constrained. The runtime knows routing is *configured*;
// it does not continuously prove a provider is reachable, so the dock states
// configuration and never liveness. The status dot is the neutral tone for the
// same reason — a success green beside "Configured" would read as a health
// check nobody performed.
//
// The orb is the Core primitive (WDS-CORE-1), the same sphere sign-in and
// onboarding show — there is one orb in the product, and a CSS lookalike in
// permanent chrome would be a second. `quiet` is the honest beat for a surface
// claiming configuration rather than work in flight, and the feed is off:
// nothing is arriving, and a mote drifting through the page head is a moving
// speck beside the page title.

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

function DockPanel({
  waiting,
  panel,
}: Readonly<{
  waiting: number | undefined;
  panel: React.RefObject<HTMLElement | null>;
}>) {
  const t = useT();
  const tools = tierCounts(useAgentTierMap());
  return (
    // A section, not a div: `aria-label` names an element only where the role
    // supports naming, so on a bare div the name is dropped and the panel is
    // unreachable by name. Named, it is a region a screen reader can land on.
    <section
      className="agentpanel"
      ref={panel}
      aria-label={t("agent.regionAria")}
    >
      <div className="agentpanelhead">
        <MarginceCoreScene
          state="quiet"
          feed={false}
          className="agentorb big"
        />
        <span className="agentwho">
          <b>{t("agent.title")}</b>
          <span className="agentstate">{t("agent.configured")}</span>
        </span>
      </div>
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
          <a className="agentrow" href="#/settings/ai">
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
  approvalsWaiting,
}: Readonly<{ approvalsWaiting?: number }>) {
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
        <MarginceCoreScene state="quiet" feed={false} className="agentorb" />
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
      {open && <DockPanel waiting={waiting} panel={panel} />}
    </div>
  );
}
