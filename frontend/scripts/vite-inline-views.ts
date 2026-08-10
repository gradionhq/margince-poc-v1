// The MCP App inliner: it folds each view's entry chunk and stylesheet into its
// document, stamps the licence header, and refuses to emit anything that reaches
// off-origin.
//
// WHY A DOCUMENT MUST BE SELF-CONTAINED. Each view declares an EMPTY origin
// allowlist, and a host builds its content-security policy from that
// declaration. "This view reaches no network" is therefore a promise kept by
// having no origin to name — so a single <link>, font URL or source-map comment
// the bundler emitted would reintroduce the origin the declaration says is not
// there, and the host would refuse it at render time with nothing here saying
// why.
//
// THERE ARE TWO CHECKS AND THEY ARE NOT ALTERNATIVES. validateDocument runs the
// EXACT token list the Go admission check embeds, over the final bytes: a
// document that passes here can then only be refused in production by tampering
// or version skew, never by the two validators disagreeing about the rules.
// inspectDocument parses the document and judges its nodes, which is what a
// substring sweep cannot do — a meta refresh carries no attribute the token list
// looks for, and `data-theme` looks exactly like `data`.

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { type HTMLElement, parse } from "node-html-parser";
import { build, type Plugin } from "vite";

/** This directory, for resolving the sibling files below whatever cwd a caller
 *  happens to have. */
const HERE = dirname(fileURLToPath(import.meta.url));

const SPDX = "SPDX-License-Identifier: BUSL-1.1";
const COPYRIGHT = "SPDX-FileCopyrightText: 2026 Gradion";

/** The shared admission vocabulary. Read from disk rather than imported so this
 *  module works identically under vitest, under a vite build and under the dev
 *  server, none of which agree about JSON import assertions. */
const VOCABULARY: Record<string, string[]> = JSON.parse(
  readFileSync(resolve(HERE, "../src/mcp-apps/forbidden.json"), "utf8"),
);

/** Element names a self-contained document has no business carrying. */
const FORBIDDEN_TAGS = [
  "link",
  "base",
  "iframe",
  "object",
  "embed",
  "form",
  "frame",
];

/** Attributes that name something to fetch. `data` is the <object> one — not
 *  `data-*`, which is where the bridge writes the theme. */
const URL_ATTRIBUTES = [
  "src",
  "srcset",
  "href",
  "poster",
  "data",
  "ping",
  "action",
  "formaction",
  "background",
  "cite",
  "manifest",
];

/**
 * validateDocument answers every forbidden token the document contains.
 *
 * An empty answer means admitted. This is the same sweep, over the same list,
 * that the api runs before it will serve a document — see the file header for
 * why that identity is the point rather than a coincidence.
 */
export function validateDocument(html: string): string[] {
  const found: string[] = [];
  for (const tokens of Object.values(VOCABULARY)) {
    for (const token of tokens) {
      if (html.includes(token)) found.push(token);
    }
  }
  return found;
}

/**
 * inspectDocument parses the document and answers what its NODES say, which is
 * the half a token list cannot reach.
 */
export function inspectDocument(html: string): string[] {
  const found: string[] = [];
  const root = parse(html, { comment: false });
  for (const node of root.querySelectorAll("*")) {
    found.push(...inspectNode(node));
  }
  return found;
}

function inspectNode(node: HTMLElement): string[] {
  const tag = node.rawTagName?.toLowerCase() ?? "";
  const found: string[] = [];
  if (FORBIDDEN_TAGS.includes(tag)) {
    found.push(`<${tag}>`);
  }
  // A meta refresh navigates the frame with no attribute any token list names.
  if (
    tag === "meta" &&
    (node.getAttribute("http-equiv") ?? "").toLowerCase() === "refresh"
  ) {
    found.push("<meta http-equiv=refresh>");
  }
  // An import map redirects every module specifier resolved after it, so it can
  // repoint an inline script at an origin the document never mentions.
  if (
    tag === "script" &&
    (node.getAttribute("type") ?? "").toLowerCase() === "importmap"
  ) {
    found.push("<script type=importmap>");
  }
  for (const attribute of URL_ATTRIBUTES) {
    if (node.hasAttribute(attribute)) {
      found.push(`${tag}[${attribute}]`);
    }
  }
  // An inline event handler binds behaviour where nothing analysing the script
  // can see it. Enumerated rather than listed: `onclick`, `onload`, `onerror`
  // and the ninety others are one RULE to a parser and an endless list to a
  // substring sweep — which is precisely what this pass is here to buy.
  //
  // Deliberately BLUNT: any attribute beginning with `on` is refused, so a
  // hypothetical `one="1"` would be refused too. That trade is the right way
  // round — a false positive costs a rename, and a miss is executable markup in
  // a sandboxed frame that no later check looks at.
  for (const attribute of Object.keys(node.attributes)) {
    if (/^on[a-z]/i.test(attribute)) {
      found.push(`${tag}[${attribute}]`);
    }
  }
  return found;
}

/**
 * inlineDocument folds the emitted entry chunk and stylesheet into the shell.
 *
 * The licence header is injected as an HTML COMMENT because there is nowhere
 * else for it to survive: esbuild strips every comment out of the script even
 * with minify off, SPDX lines included, and `legalComments` does not reach them.
 * The header on the artifact a third party receives is what honest labelling
 * actually means here.
 */
export function inlineDocument(html: string, js: string, css: string): string {
  refuseSelfClosingText(js, "script");
  const styles = stripCSSComments(css);
  refuseSelfClosingText(styles, "style");
  const root = parse(html, { comment: true });
  // Removed rather than rewritten: whatever the bundler linked is being folded
  // in, and a leftover reference is exactly what the admission check refuses.
  for (const node of root.querySelectorAll("*")) {
    const tag = node.rawTagName?.toLowerCase() ?? "";
    if (tag === "link" || (tag === "script" && node.hasAttribute("src"))) {
      node.remove();
    }
  }
  // Spliced into the SERIALIZED shell rather than parsed as nodes: a script body
  // pushed through an HTML parser is a document that depends on the parser's
  // raw-text handling being right, and this one has no reason to take that risk.
  let shell = root.toString();
  shell = spliceBefore(
    shell,
    "</head>",
    styles === "" ? "" : `<style>\n${styles}\n</style>\n`,
  );
  shell = spliceBefore(
    shell,
    "</body>",
    js === "" ? "" : `<script>\n${js}\n</script>\n`,
  );
  return stampLicence(shell);
}

/**
 * stampLicence puts the SPDX lines in an HTML comment AFTER the doctype.
 *
 * After, not before: a comment ahead of the doctype puts the browser into quirks
 * mode, which would silently change how every rule in the stylesheet above is
 * applied. The header has to be the first thing a human reads, not the first
 * thing the parser does.
 */
function stampLicence(shell: string): string {
  const doctype = /^\s*<!doctype[^>]*>/i.exec(shell);
  if (doctype === null) {
    throw new Error(
      "mcp-apps: the built document has no doctype to stamp the licence after",
    );
  }
  const header = `\n<!--\n${SPDX}\n${COPYRIGHT}\n-->`;
  return (
    shell.slice(0, doctype[0].length) + header + shell.slice(doctype[0].length)
  );
}

/**
 * spliceBefore inserts ahead of a closing tag the shell is required to have. It
 * throws rather than appending on a miss: a document whose script landed
 * somewhere other than where this claims it did is worse than a failed build.
 */
function spliceBefore(shell: string, marker: string, insert: string): string {
  if (insert === "") return shell;
  const at = shell.lastIndexOf(marker);
  if (at < 0) {
    throw new Error(`mcp-apps: the shell has no ${marker} to inline into`);
  }
  return shell.slice(0, at) + insert + shell.slice(at);
}

function stripCSSComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "").trim();
}

/**
 * refuseSelfClosingText fails the build rather than emitting a document whose
 * inline block ends early. The sequence cannot occur in valid JavaScript or CSS
 * outside a literal, so this is a real condition and not a theoretical one — and
 * escaping it silently would mean the bytes served differ from the bytes built.
 */
function refuseSelfClosingText(text: string, tag: string): void {
  if (text.toLowerCase().includes(`</${tag}`)) {
    throw new Error(
      `mcp-apps: the inline ${tag} contains "</${tag}", which would end the block early and ` +
        "leave the rest of it as page text — rewrite the literal that produces it",
    );
  }
}

export function inlineViews(): Plugin {
  return {
    name: "mcp-apps:inline-views",
    // After vite's own HTML plugin has injected the tags this folds in.
    enforce: "post",
    generateBundle(_options, bundle) {
      const html = single(bundle, (name) => name.endsWith(".html"), "document");
      const js = single(bundle, (name) => name.endsWith(".js"), "entry chunk");
      const css = single(bundle, (name) => name.endsWith(".css"), "stylesheet");
      const chunk = bundle[js];
      const style = bundle[css];
      const shell = bundle[html];
      if (
        chunk.type !== "chunk" ||
        style.type !== "asset" ||
        shell.type !== "asset"
      ) {
        throw new Error(
          "mcp-apps: the bundle's document, chunk and stylesheet are not the kinds vite emits",
        );
      }
      const inlined = inlineDocument(
        String(shell.source),
        chunk.code,
        String(style.source),
      );
      shell.source = inlined;
      delete bundle[js];
      delete bundle[css];
      // THE CARDINALITY CHECK, and it is the one that catches every asset-leak
      // class at once: a worker, a wasm module, a `new URL(…, import.meta.url)`
      // sibling, a copied public file. Each of those is a second origin-bearing
      // file the document would have to name, and none of them has its own
      // check.
      const left = Object.keys(bundle);
      if (left.length !== 1 || !left[0].endsWith(".html")) {
        throw new Error(
          `mcp-apps: the build emitted ${left.length} files (${left.join(", ")}); ` +
            "a view must be exactly one self-contained document",
        );
      }
      refuse(inlined);
    },
  };
}

/** The path each view is served at, in dev and in production alike. */
const VIEW_PATH = /^\/mcp-apps\/([a-z0-9-]+)\.html$/;

/**
 * serveMcpApps answers `/mcp-apps/<view>.html` on the DEV server with the same
 * bytes the production build emits.
 *
 * WHY THIS EXISTS. The dev server never runs `rollupOptions.input` or
 * `generateBundle`, so without it a request for a view falls through the SPA
 * fallback to a dev index.html carrying `src=` module scripts and
 * `/@vite/client` — both on the admission check's list by name. The api would
 * then refuse every document and both views would be permanently unadvertised in
 * every dev stack.
 *
 * IT RUNS THE REAL BUILD rather than reassembling the dev module graph, which is
 * the only way "the same bytes" is a fact instead of an intention: the transform
 * pipeline in dev rewrites imports and injects the HMR client, so a document
 * stitched from it would differ from the built one in exactly the ways the check
 * refuses. A build takes about a tenth of a second and the api fetches each view
 * once at startup, so nothing here is on a hot path.
 *
 * `apply: "serve"` is load-bearing: this plugin lives in vite.config.ts so
 * `make dev` reaches it, and it must contribute NOTHING to the SPA's build.
 */
export function serveMcpApps(): Plugin {
  return {
    name: "mcp-apps:serve-views",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const named = VIEW_PATH.exec(
          new URL(req.url ?? "/", "http://localhost").pathname,
        );
        if (named === null) {
          next();
          return;
        }
        buildOneView(named[1]).then(
          (document) => {
            res.setHeader("Content-Type", "text/html; charset=utf-8");
            res.setHeader("Cache-Control", "no-cache");
            res.end(document);
          },
          (err: unknown) => {
            // 500 with the reason, never a quietly-served bad document: a dev
            // server that hands over something the api will refuse teaches
            // exactly the wrong lesson about why the view never appeared.
            res.statusCode = err instanceof ViewNotFound ? 404 : 500;
            res.setHeader("Content-Type", "text/plain; charset=utf-8");
            res.end(
              `mcp-apps: ${err instanceof Error ? err.message : String(err)}`,
            );
          },
        );
      });
    },
  };
}

/** ViewNotFound separates "no such view" from "this view would not build",
 *  because the two deserve different statuses and different reading. */
class ViewNotFound extends Error {}

async function buildOneView(name: string): Promise<string> {
  const output = await build({
    configFile: resolve(HERE, "../vite.mcp-apps.config.ts"),
    mode: name,
    logLevel: "warn",
    // Nothing reaches the disk: the document is answered from memory, so a dev
    // request can never leave a stale artifact where the production build writes.
    build: { write: false },
  }).catch((err: unknown) => {
    const message = err instanceof Error ? err.message : String(err);
    throw message.includes("is not a view") ? new ViewNotFound(message) : err;
  });
  const bundles = Array.isArray(output) ? output : [output];
  for (const bundle of bundles) {
    if (!("output" in bundle)) continue;
    for (const emitted of bundle.output) {
      if (emitted.type === "asset" && emitted.fileName.endsWith(".html")) {
        return String(emitted.source);
      }
    }
  }
  throw new Error(`the build of ${name} emitted no document`);
}

/** refuse throws unless the document is admissible, naming every reason. A view
 *  that would be refused in production must not reach production. */
function refuse(html: string): void {
  const findings = [...validateDocument(html), ...inspectDocument(html)];
  if (findings.length > 0) {
    throw new Error(
      `mcp-apps: the built document reaches off-origin — ${findings.join(", ")}. ` +
        "A view declares an empty origin allowlist, so a host would refuse this at render time",
    );
  }
}

function single(
  bundle: Record<string, unknown>,
  matches: (name: string) => boolean,
  what: string,
): string {
  const names = Object.keys(bundle).filter(matches);
  if (names.length !== 1) {
    throw new Error(
      `mcp-apps: expected exactly one ${what} in the bundle, found ${names.length} (${names.join(", ")})`,
    );
  }
  return names[0];
}
