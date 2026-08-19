// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Handing the reader a file, in one spelling.
//
// There is no browser API for "save this"; there is an object URL, an anchor
// nobody sees, a click, and a revoke — four steps in an order that matters, and
// the last one is the one a second copy forgets. So the sequence lives here
// rather than in each screen that produces a file.
//
// This module is the DOM half only. What the bytes are, what the file is called,
// and whether the reader may have it at all are the caller's business.

/**
 * Save `bytes` to the reader's downloads as `filename`.
 *
 * The revoke is not optional housekeeping: an object URL pins its blob in memory
 * for the lifetime of the document, so a screen that exports repeatedly without
 * it grows a copy of every export it has ever made.
 */
export function downloadBytes(bytes: BlobPart, filename: string, type: string) {
  const url = URL.createObjectURL(new Blob([bytes], { type }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

/**
 * The filename the server asked for, or `fallback` when it named none.
 *
 * Reading the header rather than composing a name locally is what keeps a
 * download called what the export actually is: the server knows which table and
 * which format it just rendered, and a name built here would be a second guess
 * at it that drifts the moment either changes.
 *
 * Only the quoted `filename="…"` form is read, which is the form this API sends.
 * RFC 6266's `filename*` extended form carries a charset and percent-encoding,
 * and half-parsing it would produce a plausible-looking wrong name — worse than
 * falling back to one this code chose deliberately.
 */
export function filenameFromDisposition(
  disposition: string | null,
  fallback: string,
): string {
  const quoted = /filename="([^"]+)"/.exec(disposition ?? "");
  const name = quoted?.[1]?.trim();
  if (!name) {
    return fallback;
  }
  // A path separator in a download name is how a header talks a browser into
  // writing outside the downloads folder. The header is the server's, but it is
  // still input, and the leaf is the only part that is ever wanted.
  const leaf = name.split(/[/\\]/).pop() ?? "";
  return leaf === "" || leaf === "." || leaf === ".." ? fallback : leaf;
}
