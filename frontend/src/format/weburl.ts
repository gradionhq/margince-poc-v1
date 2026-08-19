// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The one place that decides whether a string may become a link.
//
// Every href a record carries is untrusted input: a crawler wrote it, a
// connector wrote it, or a person pasted it into a field whose type set has no
// `url` member. `javascript:` and `data:` in an href are script execution on
// click, and a value that is not an absolute URL at all — a bare
// `example.com`, a relative path — resolves against OUR origin, which is never
// where the record points. So only the two schemes a web address can honestly
// be are allowed through, and everything else is text.
//
// It answers with the parsed URL rather than a boolean so a caller that needs
// the normalized destination has it, and one that only needs the verdict can
// ask for a null check. What each surface DRAWS for a refused value is the
// caller's own decision — the chip keeps the fact and drops the link, the
// custom-field cell stays plain text — and the schemes are not.
export function webUrl(value: string): URL | null {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    // Unparseable as an absolute URL, which is what most values are. That is
    // the answer "this is text", not a failure to report.
    return null;
  }
  return parsed.protocol === "https:" || parsed.protocol === "http:"
    ? parsed
    : null;
}
