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
import { existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
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
// --diff-filter=d drops DELETED paths. A story file this branch removed cannot
// be rendered and is not a coverage gap — without the filter it lands in scope
// and then fails as "a changed story the build did not register", which is the
// gate reporting its own bookkeeping as a defect. The sibling spacing gate
// filters the same way.
const changed = git(`diff --name-only --diff-filter=d ${base}..HEAD`)
  .split("\n")
  .filter(Boolean);

// 2. Story-coverage map: for every source file, the story files that import it.
//    A component is covered by ANY story that renders it — coverage is not tied
//    to the story sitting at the component's own path.
//    DIRECT imports only, deliberately: a transitive graph would let one screen
//    story claim half the tree, and the gate would then render everything.
const srcRoot = join(repoRoot, "frontend/src");
const sourceExtensions = [".tsx", ".ts", ".jsx", ".js"];

function sourceFilesUnder(dir) {
  const files = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) files.push(...sourceFilesUnder(full));
    else if (entry.isFile()) files.push(full);
  }
  return files;
}

// Coverage means a story RENDERS the component, so a specifier only counts
// where the module system would honour it: comments are stripped first, and
// what remains has to sit in a real import/export position — a path mentioned
// in prose or quoted as data pulls nothing in.
const STATEMENT_START = String.raw`(?:^|[;{}])\s*`;
const IMPORT_PATTERNS = [
  // `import … from "s"` / `export … from "s"`. The clause between the keyword
  // and `from` carries neither a quote nor a `;`, which is what stops a match
  // from spanning two statements while still crossing a multi-line clause.
  new RegExp(`${STATEMENT_START}(?:import|export)\\b[^;'"]*?\\bfrom\\s*["']([^"']+)["']`, "gm"),
  // Side-effect `import "s"`.
  new RegExp(`${STATEMENT_START}import\\s+["']([^"']+)["']`, "gm"),
  // Dynamic `import("s")`, which is an expression and has no statement start.
  /\bimport\s*\(\s*["']([^"']+)["']/g,
];

// A comment or a string literal, whichever opens first. String literals are in
// the alternation so a `//` inside one — a URL, say — is consumed as part of
// the string and never read as the start of a comment.
const COMMENT_OR_STRING =
  /\/\/[^\n]*|\/\*[\s\S]*?\*\/|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`/g;

// Blanks out comments, leaving string literals alone. Blanking rather than
// deleting keeps every line and column where it was, so the statement-start
// anchors above still see real line starts.
function stripComments(source) {
  return source.replace(COMMENT_OR_STRING, (match) =>
    match.startsWith("/") ? match.replace(/[^\n]/g, " ") : match,
  );
}

// The module specifiers of `import x from "s"`, `import "s"`, `export … from "s"`
// and `import("s")` — every form by which a story pulls in a sibling module.
function importSpecifiers(source) {
  const code = stripComments(source);
  const specifiers = [];
  for (const pattern of IMPORT_PATTERNS) {
    for (const match of code.matchAll(pattern)) specifiers.push(match[1]);
  }
  return specifiers;
}

// Resolve a relative specifier to a repo-relative file the way the bundler does:
// the literal path, then the TS/JS extensions, then the directory's index.
// A bare package specifier is not repo source and resolves to null.
function resolveSpecifier(fromFile, specifier) {
  if (!specifier.startsWith(".")) return null;
  const target = resolve(dirname(join(repoRoot, fromFile)), specifier);
  const candidates = [
    target,
    ...sourceExtensions.map((ext) => `${target}${ext}`),
    ...sourceExtensions.map((ext) => join(target, `index${ext}`)),
  ];
  for (const candidate of candidates) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return relative(repoRoot, candidate);
  }
  return null;
}

const coveringStories = new Map();
for (const abs of sourceFilesUnder(srcRoot)) {
  const story = relative(repoRoot, abs);
  if (!/\.stories\.[tj]sx?$/.test(story)) continue;
  for (const specifier of importSpecifiers(readFileSync(abs, "utf8"))) {
    const imported = resolveSpecifier(story, specifier);
    if (!imported) continue;
    if (!coveringStories.has(imported)) coveringStories.set(imported, new Set());
    coveringStories.get(imported).add(story);
  }
}

// 3. In-scope story files: changed *.stories.tsx + every story that covers a
//    changed component. A changed component no story covers is a gap.
const storyFiles = new Set();
const missing = [];
for (const f of changed) {
  if (!f.startsWith("frontend/src/")) continue;
  // .d.ts declaration files are never renderable components — the generated
  // API types (src/api/schema.d.ts) regenerate on any contract change, so
  // requiring a story for them is a false gate. Skip them.
  if (f.endsWith(".d.ts")) continue;
  if (/\.stories\.[tj]sx?$/.test(f)) {
    storyFiles.add(f);
  } else if (
    /\.[tj]sx$/.test(f) &&
    !/\.d\.ts$/.test(f) &&
    !/\.(test|stories)\./.test(f)
  ) {
    const covering = new Set(coveringStories.get(f) ?? []);
    // The co-located story counts on its path alone: it may reach the component
    // through a barrel re-export rather than importing the file directly.
    const coLocated = f.replace(/\.[tj]sx?$/, ".stories.tsx");
    if (existsSync(join(repoRoot, coLocated))) covering.add(coLocated);
    if (covering.size === 0) missing.push({ component: f });
    else for (const story of covering) storyFiles.add(story);
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
    console.error(`fe-uat FAIL — changed components no story renders: [${comps}]`);
    console.error(
      "  (author a story that imports it — co-located <component>.stories.tsx is the default — then re-run)",
    );
  }
  process.exit(1);
}
const note = missing.length ? ` (allow-missing: ${missing.length})` : "";
console.log(`fe-uat OK — ${results.length} story(ies) captured → ${relative(repoRoot, outDir)}/${note}`);
