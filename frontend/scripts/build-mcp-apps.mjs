// Builds each MCP App view into exactly one self-contained document.
//
// It runs AFTER `vite build` in the one `pnpm build` command, so Dockerfile.web
// ships the views with no edit and there is no second toolchain to keep in step.
//
// Vite emits an HTML entry at its SOURCE path — dist/.mcp-apps-staging/<view>/
// src/mcp-apps/<view>/index.html — so each document is relocated to
// dist/mcp-apps/<view>.html, which is the path nginx serves and the Go catalog
// names. Without the relocation nginx would serve nothing there and the api
// would hold no view at all.

import { existsSync } from "node:fs";
import { cp, mkdir, readdir, rm } from "node:fs/promises";
import { build } from "vite";

const SOURCES = "src/mcp-apps";
const CONFIG = "vite.mcp-apps.config.ts";
const STAGING = "dist/.mcp-apps-staging";
const OUT = "dist/mcp-apps";

// A view IS a directory under src/mcp-apps carrying an index.html — the same
// rule the config applies, so a new view is built by existing rather than by
// being added to a list here and there.
const VIEWS = (await readdir(SOURCES, { withFileTypes: true }))
  .filter(
    (e) => e.isDirectory() && existsSync(`${SOURCES}/${e.name}/index.html`),
  )
  .map((e) => e.name)
  .sort();

if (VIEWS.length === 0) {
  throw new Error(
    `mcp-apps: no view found under ${SOURCES} — this build would ship nothing`,
  );
}

await rm(OUT, { recursive: true, force: true });
await rm(STAGING, { recursive: true, force: true });
await mkdir(OUT, { recursive: true });

for (const view of VIEWS) {
  const staging = `${STAGING}/${view}`;
  // --mode names the view; the config factory refuses a mode that is not one,
  // so a typo fails here rather than building an empty document.
  await build({ configFile: CONFIG, mode: view, build: { outDir: staging } });
  const emitted = await readdir(staging, {
    recursive: true,
    withFileTypes: true,
  });
  const files = emitted
    .filter((e) => e.isFile())
    .map((e) => `${e.parentPath}/${e.name}`);
  const html = files.filter((f) => f.endsWith(".html"));
  // The plugin already asserts cardinality over the BUNDLE. This asserts it over
  // what actually reached the disk, which is not the same set: anything vite
  // copies rather than bundles never appears in a bundle at all.
  if (files.length !== 1 || html.length !== 1) {
    throw new Error(
      `mcp-apps: ${view} emitted ${files.length} files (${files.join(", ")}); ` +
        "a view must be exactly one self-contained document",
    );
  }
  await cp(html[0], `${OUT}/${view}.html`);
}

await rm(STAGING, { recursive: true, force: true });
// biome-ignore lint/suspicious/noConsole: a build step reports what it produced — this line IS its output
console.log(`mcp-apps: built ${VIEWS.length} documents into ${OUT}`);
