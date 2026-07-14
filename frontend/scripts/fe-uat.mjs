// Change-scoped Storybook render + capture UAT lane. For frontend-only diffs:
// renders the CHANGED component's stories in isolation and screenshots them —
// no live stack, no DB. It is a GATE: it fails on a render error, on a changed
// component that has no story, and on a changed story the build does not
// register — and writes a machine-readable manifest a UAT runner can consume.
// Built for this repo's plain frontend/.
//
// Usage: node frontend/scripts/fe-uat.mjs [--allow-missing]
//   --allow-missing  do not fail when a changed component has no story yet
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  buildStaticStorybook,
  loadPlaywright,
  readStoryIndex,
  serveStaticStorybook,
} from "./lib/storybook-harness.mjs";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const staticDir = join(repoRoot, "frontend/storybook-static");
const outDir = join(repoRoot, ".tmp/fe-uat");
const allowMissing = process.argv.includes("--allow-missing");

// git without a shell — args split on spaces (a range like "<sha>..HEAD" is one arg).
function git(args) {
  const r = spawnSync("git", args.split(" "), { cwd: repoRoot });
  if (r.status !== 0) throw new Error(`git ${args} failed: ${r.stderr}`);
  return r.stdout.toString().trim();
}

// 1. Changed files on this branch vs origin/main.
let base;
try {
  base = git("merge-base origin/main HEAD");
} catch {
  console.error(
    "fe-uat: cannot compute merge-base with origin/main (shallow/detached?) — fall back to full-stack UAT",
  );
  process.exit(2);
}
const head = git("rev-parse HEAD");
const changed = git(`diff --name-only ${base}..HEAD`).split("\n").filter(Boolean);

// 2. In-scope story files: changed *.stories.tsx + the co-located story of any
//    changed component. A changed component with no co-located story is a gap.
const storyFiles = new Set();
const missing = [];
for (const f of changed) {
  if (!f.startsWith("frontend/src/")) continue;
  if (/\.stories\.[tj]sx?$/.test(f)) {
    storyFiles.add(f);
  } else if (/\.[tj]sx?$/.test(f) && !/\.(test|stories)\./.test(f)) {
    const story = f.replace(/\.[tj]sx?$/, ".stories.tsx");
    if (existsSync(join(repoRoot, story))) storyFiles.add(story);
    else missing.push({ component: f });
  }
}

// Map story files (frontend/src/…) to Storybook importPaths (./src/…).
const wantImportPaths = new Set([...storyFiles].map((p) => `./${p.replace(/^frontend\//, "")}`));

function writeManifest(fields) {
  mkdirSync(outDir, { recursive: true });
  writeFileSync(
    join(outDir, "manifest.json"),
    `${JSON.stringify({ base, head, ...fields }, null, 2)}\n`,
  );
}

// Empty scope (no component/story touched) → nothing to render; pass.
if (storyFiles.size === 0 && missing.length === 0) {
  writeManifest({ stories: [], missing: [], unresolved: [], pass: true });
  console.log("fe-uat OK — diff touches no component/story (empty scope)");
  process.exit(0);
}

// Render only when there are stories to capture. If the diff is purely a
// component with no story (missing), skip straight to the verdict below.
let results = [];
let unresolved = [];
if (storyFiles.size > 0) {
  // Force a FRESH build so we render the current diff — a cached build would
  // show the previous source and green-light a broken change.
  buildStaticStorybook(repoRoot, staticDir, { force: true });

  const inScope = readStoryIndex(staticDir).filter(
    (e) => e.type === "story" && wantImportPaths.has(e.importPath),
  );
  // A changed/added story file the fresh build did not register (bad glob, no
  // exported stories, malformed meta) must FAIL — never silently drop it.
  const resolvedPaths = new Set(inScope.map((e) => e.importPath));
  unresolved = [...wantImportPaths].filter((p) => !resolvedPaths.has(p));

  mkdirSync(outDir, { recursive: true });
  const { port, close } = await serveStaticStorybook(staticDir);
  const { chromium } = await loadPlaywright();
  const browser = await chromium.launch();
  const page = await browser.newPage({
    viewport: { width: 1024, height: 720 },
    deviceScaleFactor: 2,
  });

  for (const story of inScope) {
    const errors = [];
    page.removeAllListeners("pageerror");
    page.removeAllListeners("console");
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => {
      if (m.type() === "error") errors.push(m.text());
    });

    await page.goto(`http://localhost:${port}/iframe.html?id=${story.id}&viewMode=story`, {
      waitUntil: "networkidle",
    });
    let rendered = true;
    try {
      await page.waitForSelector("#storybook-root > *", { timeout: 10_000 });
    } catch {
      rendered = false;
      errors.push("#storybook-root stayed empty (component did not render)");
    }
    // Let any play() interaction settle before the frame.
    await page.waitForTimeout(250);
    const png = join(outDir, `${story.id}.png`);
    await page.screenshot({ path: png });
    const pass = rendered && errors.length === 0;
    results.push({ id: story.id, pass, png: relative(repoRoot, png), errors });
    console.log(pass ? `✓ ${story.id}` : `✗ ${story.id} — ${errors.join("; ")}`);
  }

  await browser.close();
  close();
}

const pass =
  results.every((r) => r.pass) &&
  unresolved.length === 0 &&
  (allowMissing || missing.length === 0);
writeManifest({ stories: results, missing, unresolved, pass });

if (!pass) {
  const failed = results.filter((r) => !r.pass).map((r) => r.id);
  if (failed.length) console.error(`fe-uat FAIL — stories did not render clean: [${failed.join(", ")}]`);
  if (unresolved.length)
    console.error(`fe-uat FAIL — changed story files the build did not register: [${unresolved.join(", ")}]`);
  if (!allowMissing && missing.length) {
    const comps = missing.map((m) => m.component).join(", ");
    console.error(`fe-uat FAIL — changed components with no story: [${comps}]`);
    console.error("  (author a co-located <component>.stories.tsx, then re-run)");
  }
  process.exit(1);
}
const note = missing.length ? ` (allow-missing: ${missing.length})` : "";
console.log(`fe-uat OK — ${results.length} story(ies) captured → ${relative(repoRoot, outDir)}/${note}`);
