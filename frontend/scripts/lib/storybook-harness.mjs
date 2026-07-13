// Shared harness for the Storybook capture gate (fe-uat.mjs): build the static
// Storybook, serve it locally behind a path-traversal-safe file server, resolve
// Chromium, and read the story index. Ported from the foundation skeleton and
// adapted to this repo: a plain (non-workspace) frontend/ and the Chromium that
// ships with @playwright/test — no pnpm-store glob, no extra dependency.
import { spawnSync } from "node:child_process";
import { createReadStream, existsSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, resolve, sep } from "node:path";

const MIME = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".json": "application/json",
  ".css": "text/css",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".map": "application/json",
};

// buildStaticStorybook builds storybook-static if it is absent, or
// unconditionally when force is set (fe-uat forces a fresh build so it renders
// the current diff). Runs in frontend/ — this repo has no pnpm workspace.
export function buildStaticStorybook(repoRoot, staticDir, { force = false } = {}) {
  if (existsSync(join(staticDir, "index.json")) && !force) return;
  console.log("Building static Storybook (~10-60s)…");
  const r = spawnSync("pnpm", ["exec", "storybook", "build", "-o", "storybook-static"], {
    cwd: join(repoRoot, "frontend"),
    stdio: "inherit",
  });
  if (r.status !== 0) process.exit(r.status ?? 1);
}

// serveStaticStorybook serves staticDir on an ephemeral port. The request path
// is resolved and confined to staticDir — a `..` that escapes the root is
// rejected (path-traversal safe) rather than read from disk. Returns
// {port, close}.
export function serveStaticStorybook(staticDir) {
  const root = resolve(staticDir);
  const server = createServer((req, res) => {
    const urlPath = decodeURIComponent((req.url ?? "/").split("?")[0]);
    const rel = urlPath === "/" ? "index.html" : urlPath.replace(/^\/+/, "");
    const file = resolve(root, rel);
    if (file !== root && !file.startsWith(root + sep)) {
      res.writeHead(403).end();
      return;
    }
    if (!existsSync(file)) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { "content-type": MIME[extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(res);
  });
  return new Promise((r) =>
    server.listen(0, () => r({ port: server.address().port, close: () => server.close() })),
  );
}

// loadPlaywright resolves the Chromium that ships with @playwright/test (an
// existing dev dependency — the AC/axe e2e harness uses it too), so the capture
// gate adds no new browser dependency. `pnpm exec playwright install chromium`
// provisions the binary on a fresh machine / in CI.
export async function loadPlaywright() {
  const { chromium } = await import("@playwright/test");
  return { chromium };
}

// readStoryIndex returns the story entries from the built storybook-static index.
export function readStoryIndex(staticDir) {
  return Object.values(JSON.parse(readFileSync(join(staticDir, "index.json"), "utf8")).entries);
}
