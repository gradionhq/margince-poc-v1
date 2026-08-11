import { MarginceCoreScene } from "../design-system/margince-core";
import { useT } from "../i18n";
import "./agentpanel.css";

// The agent strip: the Margince Core and everything the runtime can honestly
// say about it, read as one line at the right edge of the page head.
//
// What it may claim is constrained. The runtime knows routing is *configured*;
// it does not continuously prove a provider is reachable, so the strip states
// configuration and never liveness. The status dot is the neutral tone for the
// same reason — a success green next to "Configured" would read as a health
// check nobody performed.
//
// Activity, spend and routing are example data: the AI activity list has no
// handler behind it, and routing and spend are not wired into the shell. The
// fixture marker says so on screen rather than letting them pass as real, so it
// is never `sr-only` and never hidden while an example line it covers is still
// visible — the responsive rules in agentpanel.css retire it together with the
// last of them.
//
// The orb is the Core primitive (WDS-CORE-1), the same sphere sign-in and
// onboarding show — there is one orb in the product, and a CSS lookalike in
// permanent chrome would be a second. `quiet` is the honest beat for a surface
// claiming configuration rather than work in flight, and the feed is off:
// nothing is arriving, and a mote drifting through the page head is a moving
// speck beside the page title.
export function AgentStrip() {
  const t = useT();
  return (
    <section className="agentstrip" aria-label={t("agent.regionAria")}>
      <MarginceCoreScene state="quiet" feed={false} className="agentorb" />
      <span className="agentwho">
        <b>{t("agent.title")}</b>
        <span className="agentstate">{t("agent.configured")}</span>
      </span>
      <span className="agentactivity">{t("agent.exampleActivity")}</span>
      <b className="agentcost">{t("agent.exampleCost")}</b>
      <span className="agentrouting">{t("agent.exampleRouting")}</span>
      <span className="agentfixture t-mono">{t("agent.fixture")}</span>
    </section>
  );
}
