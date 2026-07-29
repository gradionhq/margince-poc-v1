import { CoreFeed } from "./margince-core-feed";
import { CoreLiquid } from "./margince-core-liquid";
import "./margince-core.css";

/**
 * The Margince Core — WDS-CORE-1..4 (ADR-0076).
 *
 * The product's one piece of AI identity, shown by the unauthenticated surface,
 * the session splash, onboarding and the in-app workbench. Four things about it
 * are load-bearing rather than stylistic:
 *
 *  - **One implementation** (WDS-CORE-1). A caller passes `state` and never
 *    restyles. Sizing through the documented `--coreSize` / `--coreGlass` custom
 *    properties is configuration; anything past that is a caller restyling a
 *    shared primitive.
 *  - **The state list is closed** (WDS-CORE-2), because callers use the Core as a
 *    STATUS CHANNEL — a sign-in in flight, a server that cannot be reached — and
 *    a status channel with an open vocabulary is one nobody can test and no
 *    second caller can reuse.
 *  - **Rendering is a fallback ladder, not a technology** (WDS-CORE-3): the
 *    shader is preferred, and a non-GPU CSS rendering of EVERY state is required.
 *    See `margince-core-liquid.tsx`.
 *  - **It is `aria-hidden`** (WDS-CORE-4). Every state it shows is also stated in
 *    text by the surface around it, which is what makes it safe to be this
 *    decorative.
 *
 * This replaces the CSS orbital scene (orbits, nodes, approval gate, threads, the
 * mark shell) with the liquid-glass Core the interactive mockup designs against —
 * same exported name and same props, so the three call sites did not move. What
 * changed for them: the mark no longer sits inside the sphere.
 */
export type MarginceCoreState =
  | "idle"
  | "listening"
  | "working"
  | "success"
  | "attention"
  | "error"
  | "quiet"
  | "unavailable";

export function MarginceCoreScene({
  state = "idle",
  progress,
  size = "hero",
  feed = true,
  className = "",
}: Readonly<{
  state?: MarginceCoreState;
  /** 0..1. Draws the ring; omit it and no ring renders (WDS-CORE-2). */
  progress?: number;
  size?: "hero" | "md";
  /** Context arriving at the Core. Off where nothing is arriving. */
  feed?: boolean;
  className?: string;
}>) {
  const classes = ["core", size === "md" ? "core-md" : "", className]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={classes} data-core-state={state} aria-hidden="true">
      <span className="core-glow" />
      <div className="core-tilt">
        {/* Two wrappers, both load-bearing: .core-glass paints the shell and
            .core-liquid clips the canvas to the sphere. The canvas fills
            whatever contains it, so without the clip it renders as a square. */}
        <div className="core-glass">
          <span className="core-liquid">
            <CoreLiquid state={state} />
          </span>
        </div>
      </div>
      {progress === undefined ? null : (
        <svg className="core-progress" viewBox="0 0 100 100" aria-hidden="true">
          <circle cx="50" cy="50" r="48.5" pathLength="100" />
          <circle
            className="core-progress-value"
            cx="50"
            cy="50"
            r="48.5"
            pathLength="100"
            strokeDasharray={`${Math.max(0, Math.min(1, progress)) * 100} 100`}
          />
        </svg>
      )}
      {feed ? <CoreFeed endAt={size === "md" ? 48 : 74} /> : null}
    </div>
  );
}
