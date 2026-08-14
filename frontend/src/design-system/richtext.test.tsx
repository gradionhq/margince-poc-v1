/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { plainTextOf, safeEditorHTML } from "./richtext";

// `value` is not always something a rep typed — an AI draft arrives here, and a
// model's output is untrusted input however friendly its source. The server
// filters what LEAVES for a recipient; this filters what ENTERS our document,
// and the two protect different people.
describe("what may enter the editor", () => {
  it("keeps the formatting the toolbar can produce", () => {
    const markup =
      "<p>The <b>deadline</b> is <em>Friday</em>.</p><ul><li>One</li></ul>";

    expect(safeEditorHTML(markup)).toBe(markup);
  });

  it("drops a script entirely, content included", () => {
    expect(safeEditorHTML("<p>A</p><script>alert(1)</script><p>B</p>")).toBe(
      "<p>A</p><p>B</p>",
    );
  });

  it("drops an image, because a remote one is a read receipt", () => {
    expect(
      safeEditorHTML(`<p>Hi</p><img src="https://track.test/o.gif">`),
    ).toBe("<p>Hi</p>");
  });

  it("keeps a link's text but not a javascript href", () => {
    expect(safeEditorHTML(`<a href="javascript:alert(1)">click</a>`)).toBe(
      "<a>click</a>",
    );
  });

  it("keeps http, https and mailto hrefs", () => {
    for (const href of [
      "https://gradion.com",
      "http://gradion.com",
      "mailto:x@y.test",
    ]) {
      expect(safeEditorHTML(`<a href="${href}">go</a>`)).toBe(
        `<a href="${href}">go</a>`,
      );
    }
  });

  it("unwraps an unknown element rather than losing its words", () => {
    expect(safeEditorHTML("<div>Words <b>kept</b></div>")).toBe(
      "Words <b>kept</b>",
    );
  });

  it("escapes text that looks like markup", () => {
    expect(safeEditorHTML("<p>Use &lt;b&gt; carefully</p>")).toBe(
      "<p>Use &lt;b&gt; carefully</p>",
    );
  });

  it("strips an event handler from an element it keeps", () => {
    expect(safeEditorHTML(`<p onclick="alert(1)">Text</p>`)).toBe(
      "<p>Text</p>",
    );
  });
});

// The plain part is a real alternative somebody reads, not a fallback nobody
// checks: it goes on the wire beside the markup, and a client that prefers text
// shows THIS.
describe("the plain-text rendering", () => {
  const render = (html: string) => {
    const node = document.createElement("div");
    node.innerHTML = html;
    return plainTextOf(node);
  };

  it("separates paragraphs, where textContent would run them together", () => {
    expect(render("<p>First.</p><p>Second.</p>")).toBe("First.\n\nSecond.");
  });

  it("keeps a bulleted list readable as a list", () => {
    expect(render("<ul><li>One</li><li>Two</li></ul>")).toBe("- One\n- Two");
  });

  // Inline formatting must not break a sentence. A renderer that emitted a
  // line per element would hand the plain reader a column of words.
  it("keeps a formatted sentence as one sentence", () => {
    expect(render("<p>The <b>deadline</b> is <em>Friday</em>.</p>")).toBe(
      "The deadline is Friday.",
    );
  });

  it("is empty for an empty editor", () => {
    expect(render("")).toBe("");
    expect(render("<br>")).toBe("");
  });
});
