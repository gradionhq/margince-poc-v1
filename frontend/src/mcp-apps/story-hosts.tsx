import { useEffect, useRef } from "react";
import type { Envelope, Warning } from "./types";
import "./view.css";

// The two wrappers the MCP App stories mount their subjects into.
//
// ONE FILE because it is one thing: Storybook-only scaffolding, never shipped
// UI. Keeping it together is also what lets the i18n copy rule exempt it by NAME
// rather than by a pattern — the developer-facing panel below is not copy a user
// ever reads, and the gate has no way to tell a story helper from a screen.

/**
 * The wrapper the renderer stories mount the REAL render() into. One wrapper for
 * both views, taking the renderer as a prop, because two copies of twelve lines
 * is two places for the mount to drift from what the document does.
 *
 * A CAVEAT THAT BELONGS IN THE FILE, not in a review comment: .storybook/
 * preview.tsx imports app.css, so these stories render against ambient Tailwind
 * and base element styles the standalone document does NOT have. A rule that
 * only works because app.css was loaded looks correct here and breaks inside a
 * host — the document stories beside these are what catch that.
 */
export function ViewHost({
  render,
  data,
  warnings = [],
}: {
  render: (root: HTMLElement, data: unknown, warnings: Warning[]) => void;
  data: unknown;
  warnings?: Warning[];
}) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (ref.current !== null) render(ref.current, data, warnings);
  }, [render, data, warnings]);
  return <main ref={ref} />;
}

/**
 * The wrapper the document stories put the REAL built bytes into, and then play
 * the protocol at.
 *
 * Three mechanics here are not stylistic, and each was a review finding:
 *
 *   `sandbox="allow-scripts"` with NO `allow-same-origin`. A bare srcdoc iframe
 *   inherits the embedding document's origin and is not opaque, so an
 *   unsandboxed story would not reproduce the real boundary at all — it would
 *   render a view with more privilege than any host ever grants it.
 *
 *   The story authenticates the child by RETAINING `contentWindow` and posts
 *   with `targetOrigin: "*"`. Under that sandbox the child's origin is the
 *   STRING "null", which is not a usable postMessage target, so the usual
 *   `event.source.postMessage(res, event.origin)` reply pattern fails here.
 *
 *   The built document is read through import.meta.glob rather than a static
 *   `import … from "…?raw"`. A static import of an absent file is a
 *   module-resolution error: Storybook would fail to BUILD, taking the CI
 *   frontend job down for unrelated changes — and it could never render the
 *   "run pnpm build" panel below. A glob that matches nothing is an empty
 *   object, which is what that panel exists for.
 */
export function DocumentHost({
  html,
  theme,
  answer,
  title,
}: {
  html: string | undefined;
  theme: string;
  answer: Envelope;
  title: string;
}) {
  const ref = useRef<HTMLIFrameElement>(null);
  useEffect(() => {
    const frame = ref.current;
    if (frame === null || html === undefined) return;
    const child = frame.contentWindow;
    const onMessage = (e: MessageEvent) => {
      if (e.source !== child) return;
      const msg = e.data as { id?: unknown; method?: unknown } | null;
      if (msg?.method === "ui/initialize") {
        child?.postMessage(
          { jsonrpc: "2.0", id: msg.id, result: { hostContext: { theme } } },
          "*",
        );
      }
      if (msg?.method === "ui/notifications/initialized") {
        child?.postMessage(
          {
            jsonrpc: "2.0",
            method: "ui/notifications/tool-result",
            params: { structuredContent: answer },
          },
          "*",
        );
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [html, theme, answer]);

  if (html === undefined) {
    return (
      <p>
        No built document. Run <code>pnpm build</code>, then reload.
      </p>
    );
  }
  return (
    <iframe
      ref={ref}
      title={title}
      sandbox="allow-scripts"
      srcDoc={html}
      style={{ width: "100%", height: 420, border: 0 }}
    />
  );
}

/** builtDocument answers the document `pnpm build` produced for one view, or
 *  undefined when the build has not been run. */
export function builtDocument(
  glob: Record<string, unknown>,
  view: string,
): string | undefined {
  const found = Object.entries(glob).find(([path]) =>
    path.endsWith(`/${view}.html`),
  );
  return typeof found?.[1] === "string" ? found[1] : undefined;
}
