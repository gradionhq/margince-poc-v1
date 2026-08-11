import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

// The MCP App views, driven in a real browser, asserting the one property no
// static analysis can reach: the built document makes NO network request.
//
// WHY THIS LANE EXISTS. The api's admission check is a substring ratchet and its
// own comments admit computed properties and aliases bypass it — `node['inner' +
// 'HTML']`, `window[fetchName](url)`. The build-time parsed validator judges the
// document's nodes, which is stronger, but neither can see a request that is
// only assembled at run time. A browser can.
//
// OBSERVED AT CONTEXT LEVEL, with service workers blocked. Page-level routing
// misses service-worker-handled traffic, popups and workers, which is precisely
// where a computed sink would hide.
//
// FIVE STATES, not one happy fixture. A document that fetches nothing while
// rendering a populated list may still fetch something on the empty state, on a
// warning, on a malformed payload, or when the host changes the theme.

const VIEWS = [
  { file: "account-brief" },
  { file: "relationship-map" },
] as const;

const BRIEF = {
  candidate_count: 7,
  as_of: "2026-08-10T06:00:00Z",
  items: [
    {
      deal_id: "8f14e45f-ceea-467a-9a1a-2e9b0e4c3d21",
      rank: 1,
      composite: 0.82,
      factors: {
        winnability: 0.9,
        revenue: 0.7,
        timing: 0.85,
        momentum: 0.94,
        warmth: 0.61,
      },
      state: "new",
    },
  ],
};

const NETWORK = {
  person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
  colleagues: [
    {
      display_name: "Dana Okafor",
      strength: 88,
      strength_bucket: "high",
      interactions_90d: 41,
    },
    {
      display_name: "Mira Lindqvist",
      strength_bucket: "none",
      interactions_90d: 0,
    },
  ],
};

/** The five payload/host states each view is driven through. */
function states(view: (typeof VIEWS)[number]) {
  const populated = view.file === "account-brief" ? BRIEF : NETWORK;
  const empty =
    view.file === "account-brief"
      ? { candidate_count: 0, items: [] }
      : { colleagues: [] };
  return [
    { name: "populated", data: populated, warnings: [], theme: "light" },
    { name: "empty", data: empty, warnings: [], theme: "light" },
    {
      name: "warned",
      data: populated,
      warnings: [{ code: "sweep_truncated" }],
      theme: "light",
    },
    { name: "malformed", data: "not an object", warnings: [], theme: "light" },
    { name: "theme change", data: populated, warnings: [], theme: "dark" },
  ];
}

for (const view of VIEWS) {
  test(`the ${view.file} document reaches no network in any state`, async ({
    browser,
  }) => {
    const html = await readFile(
      `dist/mcp-apps/${view.file}.html`,
      "utf8",
    ).catch(() => {
      throw new Error(
        `dist/mcp-apps/${view.file}.html is missing — this lane runs after \`pnpm build\`, ` +
          "and a silently skipped zero-request check looks exactly like a passing one",
      );
    });

    const context = await browser.newContext({ serviceWorkers: "block" });
    const requests: string[] = [];
    context.on("request", (r) => requests.push(r.url()));
    const page = await context.newPage();

    // The host: an about:blank page that frames the document under the same
    // sandbox a real host applies, and plays the protocol at it.
    await page.setContent(
      `<iframe id="view" sandbox="allow-scripts" style="width:600px;height:600px;border:0"></iframe>`,
    );
    const before = requests.length;

    for (const state of states(view)) {
      await page.evaluate(
        async ({ html, state }) => {
          const frame = document.getElementById("view") as HTMLIFrameElement;
          const child = () => frame.contentWindow;
          const ready = new Promise<void>((resolve) => {
            const onMessage = (e: MessageEvent) => {
              if (e.source !== child()) return;
              const msg = e.data as { id?: unknown; method?: unknown };
              if (msg?.method === "ui/initialize") {
                // targetOrigin "*": under this sandbox the child's origin is the
                // string "null", which is not a usable postMessage target, so the
                // usual event.source.postMessage(res, e.origin) reply cannot work.
                child()?.postMessage(
                  {
                    jsonrpc: "2.0",
                    id: msg.id,
                    result: { hostContext: { theme: state.theme } },
                  },
                  "*",
                );
              }
              if (msg?.method === "ui/notifications/initialized") {
                child()?.postMessage(
                  {
                    jsonrpc: "2.0",
                    method: "ui/notifications/tool-result",
                    params: {
                      structuredContent: {
                        data: state.data,
                        warnings: state.warnings,
                      },
                    },
                  },
                  "*",
                );
                window.removeEventListener("message", onMessage);
                resolve();
              }
            };
            window.addEventListener("message", onMessage);
          });
          // srcdoc reloads the frame, so the handshake runs fresh per state.
          frame.srcdoc = html;
          await ready;
        },
        { html, state },
      );

      // The explicit quiescence point: the rendered content is stable for 500ms.
      // Asserting straight after the post would measure a document that had not
      // yet had the chance to make the request this test is looking for.
      let previous = "";
      await expect
        .poll(
          async () => {
            const now = await page
              .frameLocator("#view")
              .locator("body")
              .innerHTML();
            const stable = now !== "" && now === previous;
            previous = now;
            return stable;
          },
          { timeout: 5_000, intervals: [500, 500, 500, 500] },
        )
        .toBe(true);
    }

    // Only the frames the host itself created. The document is srcdoc, so it is
    // never itself a request — anything counted here is the view reaching out.
    const reached = requests
      .slice(before)
      .filter((url) => !url.startsWith("about:"));
    expect(
      reached,
      `the ${view.file} view made requests: ${reached.join(", ")}`,
    ).toEqual([]);
    await context.close();
  });
}
