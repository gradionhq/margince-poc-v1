// Display-side reading of a captured mail body. The stored body is the whole
// message — signature, quoted history and all — because the enrichment pass
// mines the sign-off for contact details, so nothing may be trimmed at rest.
// What a reader wants on a timeline row is the sentence the sender wrote, and
// that is what this file finds. The rest is handed back separately and folded
// behind a control rather than dropped: a heuristic that guesses wrong must
// stay one click from being wrong in public.

export type EmailBodyParts = {
  /** The `From:/To:` preamble capture folds into the body, when present. */
  header: string;
  /** What the sender actually wrote. Never empty when the body was not. */
  main: string;
  /** Signature and quoted history, collapsed behind a control. */
  trimmed: string;
};

// The preamble capture's mail mapper prepends verbatim. Peeled before any
// heuristic runs, or the `From:` line reads as an Outlook reply header and
// folds the entire message.
const PREAMBLE = /^From:[^\n]*\nTo:[^\n]*\n\n/;

// RFC 3676 §4.3: the sign-off delimiter is a line of exactly two hyphens.
// Trailing whitespace is allowed because mail clients strip it inconsistently.
const SIGNATURE_DELIMITER = /^--\s*$/;

// Attribution lines that introduce a quoted reply. Bounded repetition keeps a
// pathological body from backtracking.
const ATTRIBUTION = [
  /^On\s.{1,200}\swrote:\s*$/,
  /^Am\s.{1,200}\sschrieb\s?.{0,120}:\s*$/,
  /^-{2,}\s*(Original Message|Ursprüngliche Nachricht|Forwarded message|Weitergeleitete Nachricht)/i,
  /^Anfang der weitergeleiteten (E-Mail|Nachricht)/i,
  /^_{5,}\s*$/,
];

// The Outlook top-reply block: a From:/Von: line whose neighbours carry the
// sent-date field. Recognised as a pair because "Von:" alone is ordinary prose
// in German mail.
const REPLY_HEADER_FROM = /^(Von|From):\s/;
const REPLY_HEADER_SENT = /^(Gesendet|Sent|Datum|Date):\s/;

// Sign-offs, matched only in the trailing region. The multi-word forms may open
// a line, since a name usually follows on the same one.
const SIGN_OFF_PREFIXES = [
  "mit freundlichen grüßen",
  "mit freundlichem gruß",
  "mit besten grüßen",
  "freundliche grüße",
  "viele grüße",
  "beste grüße",
  "herzliche grüße",
  "liebe grüße",
  "schöne grüße",
  "best regards",
  "kind regards",
  "warm regards",
  "many thanks",
  "thanks and regards",
  "diese e-mail wurde von avast",
];

// Mobile-client boilerplate. Matched against the WHOLE line rather than as a
// prefix: "Sent from my perspective, the contract is not ready" opens a
// sentence with the same three words and is the message, not the footer.
const SIGN_OFF_LINES = [
  /^von meinem (iphone|ipad|android|mobilteil|samsung)\b.*gesendet$/,
  /^sent from my \w[\w\s]{0,30}$/,
  /^gesendet mit \w[\w\s]{0,30}$/,
  /^get outlook for \w+$/,
];

// The short forms. A whole-line match only: "LG" opens a sentence about a
// washing machine as readily as it closes a mail, and "Best" mid-paragraph is
// not a sign-off at all.
const SIGN_OFF_EXACT = new Set([
  "mfg",
  "vg",
  "lg",
  "vlg",
  "best",
  "regards",
  "cheers",
  "thanks",
  "thank you",
  "danke",
  "gruß",
  "grüße",
  "bg",
]);

// How far up from the end a sign-off is looked for. A sign-off belongs to the
// closing of a mail; the same words in the opening paragraph are prose, and
// scanning the whole body folds the message away on the strength of one line.
const TRAILING_SCAN_LINES = 15;

function isSignOff(line: string): boolean {
  const normalized = line
    .trim()
    .toLowerCase()
    .replace(/[,.!]+$/, "");
  if (!normalized) {
    return false;
  }
  if (SIGN_OFF_EXACT.has(normalized)) {
    return true;
  }
  if (SIGN_OFF_LINES.some((pattern) => pattern.test(normalized))) {
    return true;
  }
  return SIGN_OFF_PREFIXES.some((prefix) => normalized.startsWith(prefix));
}

function isQuoteStart(lines: readonly string[], index: number): boolean {
  const line = lines[index] ?? "";
  if (line.startsWith(">")) {
    return true;
  }
  if (ATTRIBUTION.some((pattern) => pattern.test(line))) {
    return true;
  }
  // The Outlook block: the sent-date field within the next few lines is what
  // separates a reply header from a sentence that opens "Von:".
  if (index > 0 && REPLY_HEADER_FROM.test(line)) {
    return lines
      .slice(index + 1, index + 5)
      .some((next) => REPLY_HEADER_SENT.test(next));
  }
  return false;
}

/**
 * boundaryIndex is the first line that belongs to the tail rather than the
 * message: a quote marker anywhere, or a sign-off near the end.
 *
 * Returns -1 when the whole body is the message.
 */
function boundaryIndex(lines: readonly string[]): number {
  const signOffFloor = Math.max(0, lines.length - TRAILING_SCAN_LINES);
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i] ?? "";
    if (SIGNATURE_DELIMITER.test(line)) {
      return i;
    }
    if (isQuoteStart(lines, i)) {
      // The attribution line directly above a quoted block introduces it, so it
      // travels with the quote rather than trailing the message.
      const above = lines[i - 1]?.trim() ?? "";
      if (line.startsWith(">") && i > 0 && above.endsWith(":")) {
        return i - 1;
      }
      return i;
    }
    if (i >= signOffFloor && isSignOff(line)) {
      return i;
    }
  }
  return -1;
}

/**
 * splitEmailBody separates a stored mail body into the part worth reading on a
 * row and the tail worth keeping but not showing.
 *
 * The message is never allowed to become empty: a body that is nothing but a
 * greeting is a real message, and a heuristic that folds it away has hidden the
 * mail rather than tidied it.
 */
export function splitEmailBody(body: string): EmailBodyParts {
  if (!body.trim()) {
    return { header: "", main: "", trimmed: "" };
  }
  const preamble = PREAMBLE.exec(body);
  const header = preamble ? preamble[0].trim() : "";
  const rest = preamble ? body.slice(preamble[0].length) : body;

  // A body that is only a preamble has no message under it. The header is the
  // whole of what was captured, so it IS the message: the invariant is that a
  // non-empty body never renders as nothing.
  if (!rest.trim()) {
    return { header: "", main: body.trim(), trimmed: "" };
  }

  const lines = rest.split("\n");
  const cut = boundaryIndex(lines);
  if (cut < 0) {
    return { header, main: rest.trim(), trimmed: "" };
  }
  const main = lines.slice(0, cut).join("\n").trim();
  if (!main) {
    return { header, main: rest.trim(), trimmed: "" };
  }
  return { header, main, trimmed: lines.slice(cut).join("\n").trim() };
}

/**
 * emailSummaryText is the message on one line, for a row title or a memory
 * entry: the sender's own words, never their sign-off and never the `From:`
 * preamble.
 */
export function emailSummaryText(body: string): string {
  const { main } = splitEmailBody(body);
  const collapsed = main.replace(/\s+/g, " ").trim();
  // A body that is only a quoted forward has no message of its own. The whole
  // text is a better title than an empty one.
  return collapsed || body.replace(/\s+/g, " ").trim();
}
