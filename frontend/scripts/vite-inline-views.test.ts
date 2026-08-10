import { describe, expect, it } from "vitest";
import {
  inlineDocument,
  inspectDocument,
  validateDocument,
} from "./vite-inline-views";

const SHELL = `<!doctype html><html lang="en"><head><meta charset="utf-8" />
<title>Morning brief</title>
<script type="module" crossorigin src="/assets/index-abc.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-def.css" />
</head><body><main id="root"></main></body></html>`;

describe("the built document is validated, not trusted", () => {
  it("accepts a document with everything inline", () => {
    expect(
      validateDocument("<!doctype html><style>a{}</style><script>1</script>"),
    ).toEqual([]);
  });

  it("rejects a stylesheet link", () => {
    expect(validateDocument('<link rel="stylesheet" href="/a.css">')).toContain(
      "<link",
    );
  });

  it("rejects a source map comment", () => {
    expect(validateDocument("//# sourceMappingURL=a.map")).toContain(
      "sourceMappingURL",
    );
  });

  it("rejects dev-server residue", () => {
    expect(validateDocument('<script src="/@vite/client"></script>')).toContain(
      "/@vite/client",
    );
  });

  it("rejects a markup sink", () => {
    expect(validateDocument(`node.inner${"HTML"} = x`)).not.toEqual([]);
  });

  it("rejects a tool call, which is the widest part of the extension's surface", () => {
    expect(
      validateDocument('<script>post({method:"tools/call"})</script>'),
    ).toContain("tools/call");
  });

  it("rejects a credential, because a view is given an answer and never the means to ask again", () => {
    expect(
      validateDocument('<script>const h={Authorization:"x"}</script>'),
    ).toContain("Authorization");
  });

  it("names every token it found, not just the first", () => {
    const found = validateDocument('<link href="https://cdn.example/a.css">');
    expect(found).toContain("<link");
    expect(found).toContain("https://");
  });
});

describe("the parsed pass sees what a substring sweep cannot", () => {
  it("accepts a document whose only nodes are inline style and script", () => {
    expect(
      inspectDocument(
        "<!doctype html><html><head><style>a{}</style></head>" +
          "<body><main></main><script>1</script></body></html>",
      ),
    ).toEqual([]);
  });

  it("rejects an object that names an external file through its data attribute", () => {
    // `data`, not `data-*`: the theme attribute the bridge writes is a data-*
    // and must not be mistaken for a URL-bearing one.
    expect(
      inspectDocument('<body><object data="/x.swf"></object></body>'),
    ).not.toEqual([]);
  });

  it("does not mistake the theme attribute for a URL-bearing one", () => {
    expect(
      inspectDocument(
        '<html data-theme="dark"><body><main></main></body></html>',
      ),
    ).toEqual([]);
  });

  it("rejects a meta refresh, which navigates without any attribute a sweep looks for", () => {
    expect(
      inspectDocument(
        '<head><meta http-equiv="refresh" content="0;url=/elsewhere"></head>',
      ),
    ).not.toEqual([]);
  });

  it("rejects an import map, which redirects every later specifier", () => {
    expect(
      inspectDocument('<head><script type="importmap">{}</script></head>'),
    ).not.toEqual([]);
  });

  it("rejects a form, which posts wherever its action names", () => {
    expect(inspectDocument("<body><form></form></body>")).not.toEqual([]);
  });
});

describe("inlining folds the entry chunk and the stylesheet into the document", () => {
  it("removes the external references and leaves nothing to fetch", () => {
    const doc = inlineDocument(SHELL, "const a = 1;", ".row{border:0}");
    expect(validateDocument(doc)).toEqual([]);
    expect(inspectDocument(doc)).toEqual([]);
    expect(doc).toContain("const a = 1;");
    expect(doc).toContain(".row{border:0}");
  });

  it("injects the licence header, because esbuild strips every comment out of the script", () => {
    const doc = inlineDocument(SHELL, "const a = 1;", "");
    expect(doc).toContain("SPDX-License-Identifier: BUSL-1.1");
    expect(doc).toContain("SPDX-FileCopyrightText: 2026 Gradion");
    expect(doc.indexOf("SPDX-License-Identifier")).toBeLessThan(
      doc.indexOf("<html"),
    );
  });

  it("strips CSS comments, which carry prose the raw-read sweep would read as code", () => {
    const doc = inlineDocument(
      SHELL,
      "",
      "/* see https://example.test/why */ .row{border:0}",
    );
    expect(validateDocument(doc)).toEqual([]);
    expect(doc).not.toContain("example.test");
  });

  it("keeps the title the shell declared", () => {
    expect(inlineDocument(SHELL, "", "")).toContain(
      "<title>Morning brief</title>",
    );
  });

  it("refuses a script that would close its own tag rather than emitting a broken document", () => {
    expect(() => inlineDocument(SHELL, 'const a = "</script>";', "")).toThrow(
      /script/i,
    );
  });

  it("refuses a stylesheet that would close its own tag", () => {
    expect(() =>
      inlineDocument(SHELL, "", '.a::after{content:"</style>"}'),
    ).toThrow(/style/i);
  });
});
