import { createServer, type Plugin, type ViteDevServer } from "vite";
import { afterAll, beforeAll, expect, it } from "vitest";
import spaConfig from "../vite.config";
import {
  inspectDocument,
  serveMcpApps,
  validateDocument,
} from "./vite-inline-views";

let server: ViteDevServer;

beforeAll(async () => {
  server = await createServer({
    configFile: false,
    plugins: [serveMcpApps()],
    server: { port: 5199, strictPort: true },
    // Nothing here serves the SPA, so crawling its entry to pre-bundle
    // dependencies is work this spec neither needs nor can do without the SPA's
    // own plugins.
    optimizeDeps: { noDiscovery: true },
    logLevel: "error",
  });
  await server.listen();
});

afterAll(async () => {
  await server.close();
});

it("serves a document the admission check would accept", async () => {
  const res = await fetch("http://localhost:5199/mcp-apps/account-brief.html");
  expect(res.status).toBe(200);
  expect(res.headers.get("content-type")).toMatch(/text\/html/);
  const doc = await res.text();
  expect(validateDocument(doc)).toEqual([]);
  expect(inspectDocument(doc)).toEqual([]);
  expect(doc).toContain("<title>Morning brief</title>");
  expect(doc).toContain("SPDX-License-Identifier: BUSL-1.1");
}, 30_000);

it("404s a view that does not exist rather than answering with something else", async () => {
  // The production posture, mirrored: nginx's try_files ... =404 exists because
  // a fallback answering 200 would hand the api an app shell it would then
  // believe was a view.
  const res = await fetch("http://localhost:5199/mcp-apps/nope.html");
  expect(res.status).toBe(404);
}, 30_000);

it("is wired into the dev server `make dev` actually starts", () => {
  // Without this the middleware above is a facility nothing reaches: `make dev`
  // runs vite.config.ts, not the mcp-apps build config, so a request for a view
  // would fall through the SPA fallback to a dev index.html carrying `src=`
  // module scripts and /@vite/client — both refused by name, leaving both views
  // permanently unadvertised in every dev stack.
  expect(pluginNames(spaConfig.plugins)).toContain("mcp-apps:serve-views");
});

/** A plugin option is a plugin, a nested array of them, a promise, or nothing —
 *  so the names are collected by walking rather than by flattening, which TypeScript
 *  cannot type for an arbitrarily nested recursive union. */
function pluginNames(options: unknown): string[] {
  if (Array.isArray(options)) return options.flatMap(pluginNames);
  if (typeof options !== "object" || options === null) return [];
  const named = options as Partial<Plugin>;
  return typeof named.name === "string" ? [named.name] : [];
}
