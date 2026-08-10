// The App-side half of the MCP Apps postMessage transport (SEP-1865,
// 2026-01-26): a view is an MCP client that reaches its host through
// window.parent rather than through a socket.
//
// It is imported by EVERY view and folded into each built document, so the
// handshake exists once. A per-view copy would be two implementations of one
// protocol, and the failure they would eventually differ on is the one where a
// view renders nothing because it never announced itself.
//
// WHAT THIS FILE REFUSES TO DO, and why each refusal is load-bearing:
//
//   It never builds DOM from a string. Everything a view displays arrives in
//   `structuredContent`, which is customer data — a person's name, a note
//   someone pasted, the subject line of an ingested email. That is untrusted
//   text by this system's own reckoning, and the view runs inside a sandbox
//   whose whole job is to contain it. Assigning that text as MARKUP would hand
//   it the one privilege the sandbox cannot take back: execution inside the
//   view's own origin. So text reaches the page only through textContent, and
//   structure only through createElement.
//
//   The markup-assignment properties are not named here, deliberately: the
//   admission check reads the built document, and prose that spelled one would
//   trip a check on the very thing it was explaining.
//
//   It holds no credential. A view is given a tool's ANSWER, never the means to
//   ask again. There is no token here to leak, which is a stronger property
//   than a token handled carefully.
//
//   It calls no tool. The protocol permits it; nothing here needs it, and it is
//   the widest part of the extension's surface. A view that stays a renderer
//   cannot become a second door onto a record.

import {
  asFiniteNumber,
  asRecord,
  asText,
  asWarnings,
  type Warning,
} from "./types";

const PROTOCOL_VERSION = "2026-01-26";

/** What a view shows for a value it does not have. Never "NaN", never "0". */
const ABSENT = "—";

/** The handler a view registers, called once per tool result the host pushes. */
type ResultHandler = (data: unknown, warnings: Warning[]) => void;

let nextID = 1;
let resultHandler: ResultHandler | null = null;
// The id our own ui/initialize was sent under, cleared as it is consumed.
let initializeID: number | null = null;
// Whether the handshake has completed. A view is only supposed to be given a
// result after it has announced itself, and these two states are what let
// handle() refuse anything out of order.
let initialized = false;
// The host's origin, LEARNED rather than configured.
//
// A view is loaded into an opaque sandbox origin and cannot know its host's
// ahead of time — reading window.parent.origin throws cross-origin, and the
// specification therefore prescribes '*' for the opening message. But the
// response to it arrives with the host's origin attached, so from that point on
// there IS something to pin: every later message is sent to that origin and
// accepted only from it.
//
// 'null' is kept as a sentinel rather than pinned. An opaque origin reports
// itself as the STRING "null", which is not a usable postMessage target, so a
// host in one keeps the wildcard — the sender check below is what holds there.
let hostOrigin: string | null = null;

/**
 * fromHost checks every inbound message on TWO things: it came from the frame
 * that embedded us, and — once the host's origin is known — from that origin.
 *
 * The sender check is the stronger of the two and it holds from the first
 * message: a sandboxed view can still be messaged by anything holding a handle
 * to its window, and a view that rendered whatever arrived would let a second
 * sender choose what the human is shown. The origin check adds what the sender
 * check cannot see, which is a host that navigated the parent frame somewhere
 * else between the handshake and the result.
 */
function fromHost(event: MessageEvent): boolean {
  if (event.source !== window.parent) return false;
  if (hostOrigin === null || hostOrigin === "null") return true;
  return event.origin === hostOrigin;
}

/**
 * send posts one message to the host, pinned to its origin once that is known
 * and '*' only for the opening message, which is sent before there is anything
 * to learn it from.
 *
 * Nothing sensitive travels outward on either path: what this view sends is a
 * handshake — an initialise request naming the protocol revision, and its
 * confirmation. No record, no credential, no customer text ever leaves here,
 * because a view is given an answer and never the means to ask again.
 */
function send(message: Record<string, unknown>): void {
  const target =
    hostOrigin === null || hostOrigin === "null" ? "*" : hostOrigin;
  window.parent.postMessage({ jsonrpc: "2.0", ...message }, target);
}

function announce(): number {
  const id = nextID++;
  send({
    id,
    method: "ui/initialize",
    params: {
      protocolVersion: PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: "margince-view", version: "1" },
    },
  });
  return id;
}

/**
 * applyTheme follows the way round the host says it is drawn. Following it is
 * the whole reason a view looks embedded rather than pasted in.
 *
 * A HOST THAT STATES NOTHING IS NOT A HOST THAT IS LIGHT. `hostContext.theme` is
 * optional, and the design tokens answer only `[data-theme="dark"]` — the SPA
 * always stamps the attribute itself, so the canon never needed a media arm. A
 * view has no such guarantee, and left unstamped it would render permanently
 * light inside a dark host.
 *
 * So the resolution is the one src/app/theme.ts already makes for the same
 * failure on the unauthenticated surface: an explicit statement wins, and the
 * platform preference answers when there is none. The attribute is ALWAYS set,
 * which is what lets this document carry no second copy of the dark palette —
 * one visual language, defined once, in tokens.css.
 */
function applyTheme(hostContext: unknown): void {
  const stated = asText(asRecord(hostContext).theme);
  document.documentElement.dataset.theme =
    stated !== "" ? stated : platformTheme();
  // A host that stated nothing has delegated to the platform, so a reader who
  // flips their system appearance mid-session is followed rather than left on
  // the theme that was current when the panel opened. A host that DID state one
  // has decided, and its decision is not second-guessed.
  if (stated === "") {
    darkPreference()?.addEventListener("change", followPlatform);
  }
}

/** followPlatform repaints on a platform appearance change. Named rather than
 *  inline so it stays one listener however many results arrive. */
function followPlatform(): void {
  document.documentElement.dataset.theme = platformTheme();
}

/** darkPreference answers null where matchMedia is unavailable, which is a
 *  default rather than an error — the same posture theme.ts takes on storage. */
function darkPreference(): MediaQueryList | null {
  if (typeof window.matchMedia !== "function") return null;
  return window.matchMedia("(prefers-color-scheme: dark)");
}

function platformTheme(): string {
  return darkPreference()?.matches === true ? "dark" : "light";
}

/**
 * completeHandshake answers the response to our own ui/initialize, which is the
 * readiness signal: the host is listening, so we confirm and then wait to be
 * given a result.
 *
 * The id is CLEARED as it is consumed, so a repeated response cannot make the
 * view announce itself twice. A host that received two
 * ui/notifications/initialized would be entitled to read the second as a fresh
 * view and re-send everything it had already sent.
 */
function completeHandshake(
  event: MessageEvent,
  message: Record<string, unknown>,
): void {
  initializeID = null;
  initialized = true;
  // Learned here and only here, from the one message whose sender is already
  // proven to be the embedding frame.
  hostOrigin = event.origin;
  applyTheme(asRecord(message.result).hostContext);
  send({ method: "ui/notifications/initialized", params: {} });
}

/**
 * deliverResult hands one tool answer to the view's renderer.
 *
 * `params` IS the CallToolResult — `structuredContent` sits directly on it, not
 * under a nested member — and that structuredContent is the envelope every tool
 * on this surface seals its answer into: `data` is the tool's own result,
 * `warnings` are the conditions the answer came with.
 *
 * THE WARNINGS ARE PASSED ON, not dropped. A bounded read reports its bound as a
 * warning rather than in its data — `who_knows` stops at a cap and says so — so
 * a renderer given only `data` would present a truncated list as the whole
 * network. That is the one thing the tool's own contract forbids in those words.
 */
function deliverResult(message: Record<string, unknown>): void {
  if (resultHandler === null) return;
  const envelope = asRecord(asRecord(message.params).structuredContent);
  resultHandler(envelope.data ?? null, asWarnings(envelope.warnings));
}

function handle(event: MessageEvent): void {
  if (!fromHost(event)) return;
  const message = asRecord(event.data);
  if (message.jsonrpc !== "2.0") return;
  // `"result" in message` rather than a truthy check: `"result": null` is a
  // legal JSON-RPC response, and treating it as "not answered yet" left the view
  // permanently blank — no re-announce, no timeout, every later result dropped,
  // and nothing anywhere saying why.
  if (
    initializeID !== null &&
    message.id === initializeID &&
    "result" in message
  ) {
    completeHandshake(event, message);
    return;
  }
  // A result BEFORE the handshake is dropped rather than rendered. The view has
  // not been told the theme or the display mode yet, so rendering then shows the
  // human a panel drawn against defaults the host already corrected — and
  // accepting data outside the sequence is how a view ends up rendering whatever
  // arrives whenever it arrives.
  if (message.method === "ui/notifications/tool-result" && initialized) {
    deliverResult(message);
  }
}

/**
 * onResult registers the view's renderer. `warnings` is always an array, so a
 * renderer never has to decide whether an absent one means "none" or "not
 * computed".
 */
export function onResult(fn: ResultHandler): void {
  resultHandler = fn;
}

/**
 * warned reports whether one condition was raised. The codes belong to the
 * envelope, so a view asks by code rather than reading prose it would then have
 * to keep in step with the server's wording.
 */
export function warned(warnings: Warning[], code: string): boolean {
  return warnings.some((w) => w.code === code);
}

/**
 * el is the ONLY way anything reaches the page, and it takes text rather than
 * markup.
 */
export function el(
  tag: string,
  className?: string,
  text?: string | number,
): HTMLElement {
  const node = document.createElement(tag);
  if (className !== undefined) node.className = className;
  if (text !== undefined) node.textContent = String(text);
  return node;
}

/**
 * percent renders a number a view displays as a proportion. Anything that is not
 * a finite number renders as an em dash rather than as "NaN" or "undefined": a
 * view is looking at data it did not produce, and a missing field is a thing
 * that happens.
 */
export function percent(value: unknown): string {
  const n = asFiniteNumber(value);
  return n === null ? ABSENT : `${Math.round(n * 100)}%`;
}

/** count renders a whole number, or the em dash for a value it cannot read. */
export function count(value: unknown): string {
  const n = asFiniteNumber(value);
  return n === null ? ABSENT : String(n);
}

/**
 * money renders an amount the way the product renders one: integer MINOR units
 * scaled by the currency's own minor-unit count, never by a hard-coded 100.
 *
 * THE SCALING RULE IS THE ONE src/format/format.ts APPLIES, deliberately —
 * ask Intl how many fraction digits the currency has and divide by that power
 * of ten. JPY stores 1234 minor units and means ¥1,234; a view that divided by
 * 100 everywhere would render ¥12.34 for it, and the same class of mistake in
 * the other direction is what made an account brief report every deal a
 * hundred times too large.
 *
 * It is a second implementation rather than an import because formatMoney
 * takes the SPA's Locale, and reaching for that would pull the translation
 * machinery into a document that is inlined whole and served to a third-party
 * host. So the locale is the host runtime's own, and the rule that must not
 * drift — the scale — is stated here in the same terms.
 *
 * An amount that is not a finite number, or a currency Intl does not know,
 * renders as the em dash. Intl throws on an unknown currency code, and a view
 * that threw mid-render would leave the reader a blank panel.
 *
 * SO DOES AN AMOUNT OUTSIDE THE SAFE INTEGER RANGE. The field is an int64 on
 * the wire and a double by the time this sees it, so a value past 2^53 has
 * already been rounded to a number that is not the one that was stored. There
 * is nothing to recover — the digits are gone before this function is called —
 * and the choice is between an em dash and a money figure that is quietly
 * wrong. A reader can act on the first.
 */
export function money(amountMinor: unknown, currency: unknown): string {
  const minor = asFiniteNumber(amountMinor);
  const code = asText(currency);
  if (minor === null || code === "" || !Number.isSafeInteger(minor))
    return ABSENT;
  try {
    const formatter = new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: code,
    });
    const digits = formatter.resolvedOptions().maximumFractionDigits ?? 2;
    return formatter.format(minor / 10 ** digits);
  } catch {
    return ABSENT;
  }
}

/**
 * day renders an instant as the calendar day it falls on IN UTC, which is the
 * same day the server's own evidence snippets name.
 *
 * UTC and not the reader's zone, because the two would disagree: the answer
 * says a promise is overdue, and a date rendered in a zone the server did not
 * judge in can read as "due tomorrow" beside the word "overdue". One clock,
 * one day, and the state beside it is true of the date shown.
 */
export function day(value: unknown): string {
  const text = asText(value);
  if (text === "") return ABSENT;
  const at = new Date(text);
  if (Number.isNaN(at.getTime())) return ABSENT;
  return at.toISOString().slice(0, 10);
}

/**
 * initBridge attaches the transport and announces this view to its host.
 *
 * It runs on import, which is what makes a view a client the moment its document
 * is loaded: a host may push a result immediately after the frame is created,
 * and a bridge waiting to be started by hand would miss it.
 */
export function initBridge(): void {
  window.addEventListener("message", handle);
  initializeID = announce();
}

initBridge();
