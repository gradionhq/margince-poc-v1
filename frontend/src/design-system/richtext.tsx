// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Bold, Italic, Link2, List, ListOrdered } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import "./richtext.css";

/**
 * RichText — the light formatting a business email needs, and nothing more.
 *
 * Bold, italic, links and lists. Not a document editor: the message this writes
 * goes through the server's outbound allowlist
 * (`activities.SanitizeOutboundHTML`), which keeps exactly this set and unwraps
 * everything else, so a toolbar offering headings or colours would offer
 * formatting the recipient never receives.
 *
 * ## Why contentEditable rather than an editor library
 *
 * A library (tiptap, Lexical) buys a document model, collaborative editing and
 * a plugin system — none of which a five-sentence email needs — for a dependency
 * tree this product would then carry forever. What it would genuinely buy is
 * paste normalisation, and the server already does that job for the case that
 * matters: what a recipient receives is what the allowlist admits, whatever the
 * browser put in the DOM.
 *
 * ## The two values
 *
 * Every change reports BOTH renderings: `html` for the markup alternative and
 * `text` for the plain part. They are the same message in two forms and both go
 * on the wire (multipart/alternative), so a caller that kept only one would send
 * a message whose halves disagree — and which half a recipient reads is their
 * client's decision, not ours.
 *
 * ## What it does not do
 *
 * No image button: this product refuses tracking pixels, and a remote image is
 * a read receipt. No colour or font controls: they arrive as inline styles the
 * allowlist drops, so the button would lie.
 */
export function RichText({
  value,
  onChange,
  label,
  labels,
  placeholder,
  rows = 12,
  id,
}: Readonly<{
  /**
   * The markup to show. Read on mount and when it changes from OUTSIDE — an
   * AI draft arriving, a form reset — never on every keystroke, which would
   * fight the caret.
   */
  value: string;
  onChange: (next: { html: string; text: string }) => void;
  label: string;
  /**
   * The toolbar's accessible names. Copy never lives in a primitive: words
   * arrive through props, translated by the caller with t().
   */
  labels: Readonly<{
    bold: string;
    italic: string;
    bulletList: string;
    numberList: string;
    link: string;
    linkPrompt: string;
  }>;
  placeholder?: string;
  rows?: number;
  id?: string;
}>) {
  const generatedId = useId();
  const fieldId = id ?? generatedId;
  const editor = useRef<HTMLDivElement>(null);
  // What we last handed the caller, or last wrote into the node. Comparing
  // against it tells an outside change (a draft arriving) from the echo of our
  // own keystroke.
  //
  // It starts EMPTY rather than at `value`, and that is the whole point: seeded
  // with `value`, the effect below saw no difference on mount and never wrote
  // the node — so a composer reopened with a message in state rendered blank
  // while Send still carried the invisible text.
  const [ours, setOurs] = useState("");

  useEffect(() => {
    const node = editor.current;
    if (!node || value === ours) {
      return;
    }
    // Filtered before it reaches OUR document, not only before it reaches a
    // recipient's. The server's allowlist governs what goes out; this one
    // governs what a model's draft may put in the page a rep is looking at,
    // which is a different trust boundary with a different victim.
    node.innerHTML = safeEditorHTML(value);
    setOurs(node.innerHTML);
  }, [value, ours]);

  const report = () => {
    const node = editor.current;
    if (!node) {
      return;
    }
    const html = node.innerHTML;
    setOurs(html);
    onChange({ html, text: plainTextOf(node) });
  };

  const apply = (command: string) => {
    editor.current?.focus();
    // execCommand is deprecated and still the only cross-browser way to apply
    // formatting to a selection without a document model. The alternative is
    // reimplementing selection surgery, which is where hand-rolled editors go
    // wrong; what it produces is bounded by the server's allowlist anyway.
    document.execCommand(command);
    report();
  };

  const addLink = () => {
    const href = window.prompt(labels.linkPrompt);
    if (href === null) {
      return;
    }
    editor.current?.focus();
    // An empty answer REMOVES the link, which is the only way to undo one from
    // a toolbar with no unlink button.
    document.execCommand(
      href.trim() === "" ? "unlink" : "createLink",
      false,
      href.trim(),
    );
    report();
  };

  return (
    <div className="richtext">
      <div className="richtext-bar" role="toolbar" aria-label={label}>
        <RichTextButton onClick={() => apply("bold")} title={labels.bold}>
          <Bold size={15} aria-hidden="true" />
        </RichTextButton>
        <RichTextButton onClick={() => apply("italic")} title={labels.italic}>
          <Italic size={15} aria-hidden="true" />
        </RichTextButton>
        <RichTextButton
          onClick={() => apply("insertUnorderedList")}
          title={labels.bulletList}
        >
          <List size={15} aria-hidden="true" />
        </RichTextButton>
        <RichTextButton
          onClick={() => apply("insertOrderedList")}
          title={labels.numberList}
        >
          <ListOrdered size={15} aria-hidden="true" />
        </RichTextButton>
        <RichTextButton onClick={addLink} title={labels.link}>
          <Link2 size={15} aria-hidden="true" />
        </RichTextButton>
      </div>
      {/* biome-ignore lint/a11y/useSemanticElements: a textarea cannot carry formatting; this is the editable surface the toolbar acts on */}
      <div
        ref={editor}
        id={fieldId}
        role="textbox"
        aria-multiline="true"
        aria-label={label}
        contentEditable
        suppressContentEditableWarning
        // contentEditable is focusable in every engine, but stating it is what
        // makes the role and the behaviour agree for a checker and a reader.
        tabIndex={0}
        data-placeholder={placeholder}
        className="richtext-input"
        style={{ minHeight: `${rows * 1.5}em` }}
        onInput={report}
        onBlur={report}
      />
    </div>
  );
}

function RichTextButton({
  onClick,
  title,
  children,
}: Readonly<{
  onClick: () => void;
  title: string;
  children: React.ReactNode;
}>) {
  return (
    <button
      type="button"
      className="richtext-btn"
      // The pointer-down default is what steals the selection the command is
      // about to act on, so the button never takes focus from the text.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
      title={title}
      aria-label={title}
    >
      {children}
    </button>
  );
}

/**
 * The plain-text rendering of what the editor holds.
 *
 * Not `textContent`: that runs every block together, so three paragraphs arrive
 * as one sentence with no space between them. Block boundaries become newlines
 * and a list item keeps its marker, because the plain part is a real
 * alternative somebody reads rather than a fallback nobody checks.
 */
export function plainTextOf(node: HTMLElement): string {
  const lines: string[] = [];
  // The line being built. Inline formatting must NOT break it: "The <b>deadline
  // </b> is Friday" is one sentence, and a renderer that emitted a line per
  // element would hand the plain reader a column of words.
  let current = "";
  const flush = () => {
    if (current.trim() !== "") {
      lines.push(current.trim());
    }
    current = "";
  };
  const walkListItem = (element: HTMLElement, prefix: string) => {
    flush();
    // An ordered list numbers its items and an unordered one bullets them.
    // Emitting neither leaves the plain reader a list that is not a list.
    const ordered = element.parentElement?.tagName.toLowerCase() === "ol";
    current = `${prefix}${ordered ? `${itemNumber(element)}. ` : "- "}`;
    walk(element, `${prefix}  `);
    flush();
  };
  const walkLink = (element: HTMLElement, prefix: string) => {
    // The destination is the point of a link, and a text client shows no href —
    // so the URL rides beside the label rather than being lost.
    walk(element, prefix);
    const href = element.getAttribute("href") ?? "";
    if (href !== "" && !current.includes(href)) {
      current += ` <${href}>`;
    }
  };
  const walkElement = (element: HTMLElement, prefix: string) => {
    const tag = element.tagName.toLowerCase();
    if (tag === "br") {
      flush();
    } else if (tag === "li") {
      walkListItem(element, prefix);
    } else if (tag === "a") {
      walkLink(element, prefix);
    } else if (isBlock(tag)) {
      flush();
      walk(element, prefix);
      flush();
      lines.push("");
    } else {
      // Inline: the line continues, which keeps a formatted sentence one
      // sentence.
      walk(element, prefix);
    }
  };
  const walk = (parent: Node, prefix: string) => {
    for (const child of Array.from(parent.childNodes)) {
      if (child.nodeType === Node.TEXT_NODE) {
        current += child.textContent ?? "";
      } else if (child instanceof HTMLElement) {
        walkElement(child, prefix);
      }
    }
  };
  walk(node, "");
  flush();
  return lines
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

/**
 * The subset of markup this editor will render.
 *
 * `value` is not always something a rep typed: an AI draft arrives here, and a
 * model's output is untrusted input however friendly its source. The server
 * filters what LEAVES for a recipient; this filters what ENTERS our own
 * document, and the two protect different people.
 *
 * It mirrors the server's allowlist deliberately — the same elements, the same
 * three link schemes — so a rep never sees formatting in the composer that the
 * outbound filter would strip on the way out. Built with the DOM parser rather
 * than a regex: the browser is the thing that decides what markup means, so it
 * is the thing that should parse it.
 */
export function safeEditorHTML(markup: string): string {
  const allowed = new Set([
    "P",
    "BR",
    "B",
    "STRONG",
    "I",
    "EM",
    "U",
    "UL",
    "OL",
    "LI",
    "A",
    "BLOCKQUOTE",
    "HR",
  ]);
  const dropped = new Set([
    "SCRIPT",
    "STYLE",
    "IFRAME",
    "OBJECT",
    "EMBED",
    "FORM",
    "INPUT",
    "BUTTON",
    "SELECT",
    "TEXTAREA",
    "TEMPLATE",
    "NOSCRIPT",
    "TITLE",
    "LINK",
    "META",
    "IMG",
  ]);
  const parsed = new DOMParser().parseFromString(markup, "text/html");
  const cleanElement = (element: Element): string => {
    const tag = element.tagName;
    if (dropped.has(tag)) {
      return "";
    }
    if (!allowed.has(tag)) {
      // Unwrap, exactly as the server does: a sender whose <div> vanished still
      // meant the sentence inside it.
      return clean(element);
    }
    const lower = tag.toLowerCase();
    if (lower === "br" || lower === "hr") {
      return `<${lower}>`;
    }
    const href = tag === "A" ? safeHref(element.getAttribute("href")) : "";
    const attr = href ? ` href="${escapeText(href)}"` : "";
    return `<${lower}${attr}>${clean(element)}</${lower}>`;
  };
  const clean = (parent: Node): string => {
    let out = "";
    for (const child of Array.from(parent.childNodes)) {
      if (child.nodeType === Node.TEXT_NODE) {
        out += escapeText(child.textContent ?? "");
      } else if (child instanceof Element) {
        out += cleanElement(child);
      }
    }
    return out;
  };
  return clean(parsed.body);
}

// Which item this is within its own list, counting only siblings — so a nested
// list restarts rather than continuing its parent's numbering.
function itemNumber(item: HTMLElement): number {
  let n = 1;
  for (
    let prev = item.previousElementSibling;
    prev !== null;
    prev = prev.previousElementSibling
  ) {
    if (prev.tagName.toLowerCase() === "li") {
      n += 1;
    }
  }
  return n;
}

// A block element ends the line it was on; an inline one continues it.
function isBlock(tag: string): boolean {
  return (
    tag === "p" ||
    tag === "div" ||
    tag === "ul" ||
    tag === "ol" ||
    tag === "blockquote"
  );
}

function safeHref(href: string | null): string {
  const trimmed = (href ?? "").trim();
  const lowered = trimmed.toLowerCase();
  const ok = ["http://", "https://", "mailto:"].some((scheme) =>
    lowered.startsWith(scheme),
  );
  return ok ? trimmed : "";
}

function escapeText(text: string): string {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
