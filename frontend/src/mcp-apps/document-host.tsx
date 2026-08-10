import { useEffect, useRef } from "react";
import type { Envelope } from "./types";

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
 *   The built document is read LAZILY through import.meta.glob. A static
 *   `import … from "…?raw"` of an absent file is a module-resolution error:
 *   Storybook would fail to build, taking the CI frontend job down for unrelated
 *   changes — and it could never render the "run pnpm build" panel below.
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
