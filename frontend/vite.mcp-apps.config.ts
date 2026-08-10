import { existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type UserConfig } from "vite";
import { inlineViews } from "./scripts/vite-inline-views";

// The MCP App views' own build. vite.config.ts is UNTOUCHED, and deliberately:
// it has no `build` section at all today, so every pin below — the browser
// target, module preloading, asset emission — would have changed the SPA's
// output rather than only these documents'.
//
// ONE SINGLE-INPUT BUILD PER VIEW, and that is not a stylistic choice. Rollup
// refuses multiple inputs when inlineDynamicImports is set, and a two-entry
// build would additionally let it extract bridge.ts and view.css into chunks
// SHARED between the views — which the inliner would then have to traverse. One
// build per view removes the sharing rather than teaching the inliner about it.

/** A view IS a directory under src/mcp-apps carrying an index.html. Derived
 *  rather than listed, so the build driver and this config cannot come to
 *  disagree about which views exist, and a new one needs no registration. */
export function isView(name: string): boolean {
  return existsSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "src/mcp-apps",
      name,
      "index.html",
    ),
  );
}

export function mcpAppView(name: string, outDir: string): UserConfig {
  if (!isView(name)) {
    throw new Error(
      `mcp-apps: "${name}" is not a view — this build is selected by --mode, and no ` +
        `src/mcp-apps/${name}/index.html exists`,
    );
  }
  return {
    // No absolute asset URLs: this document declares an empty origin allowlist.
    base: "",
    // public/ bypasses transforms and is copied unchanged — a file there would
    // land beside the document and break the one-file cardinality rule.
    publicDir: false,
    esbuild: {
      // esbuild strips every comment even with minify off, SPDX included, so
      // there is nothing for legalComments to preserve. Stated so the inliner's
      // injected header reads as the deliberate answer it is.
      legalComments: "none",
      target: "esnext",
    },
    build: {
      outDir,
      emptyOutDir: true,
      copyPublicDir: false,
      // Unminified: this is a document a third party receives and may read.
      minify: false,
      // A source map is a second file AND a URL comment in the first one.
      sourcemap: false,
      modulePreload: false,
      cssCodeSplit: false,
      manifest: false,
      ssrManifest: false,
      // Every referenced asset folds into the code rather than landing beside
      // the document as a file with an origin.
      assetsInlineLimit: () => true,
      rollupOptions: {
        input: `src/mcp-apps/${name}/index.html`,
        output: { inlineDynamicImports: true },
      },
    },
    plugins: [inlineViews()],
  };
}

// Selected by `--mode <view>` so vite loads and transpiles this file itself.
// The build driver is plain .mjs and cannot import the factory above directly:
// Node's type stripping would not resolve this file's own extensionless import
// of the plugin.
export default defineConfig(({ mode }) =>
  mcpAppView(mode, `dist/.mcp-apps-staging/${mode}`),
);
