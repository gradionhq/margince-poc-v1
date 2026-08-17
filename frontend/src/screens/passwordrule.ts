// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/**
 * The password rule the client states, in one place.
 *
 * It was in three: `passwordcard.tsx`, `setupclaim.tsx` and `auth.tsx` each
 * declared `MIN_PASSWORD = 12`, each commented as "the floor the server applies,
 * restated". Three copies of a number the server owns is a lesser problem than
 * what sat under them — the three did not agree on what a character IS.
 * `auth.tsx` measured `password.length`, which counts UTF-16 code units, while
 * the other two measured `[...password].length`, which counts code points. A
 * password carrying an emoji or any astral character therefore passed on the
 * reset screen and was refused on the change screen, or the reverse, depending
 * on which side of twelve it landed. The server has one rule; the client had
 * two, and neither screen could see the other.
 *
 * Code points is the reading kept, because it is what a person means by "twelve
 * characters" — a single emoji is one character to the reader who typed it, and
 * counting it as two lets a password that LOOKS eleven characters long pass.
 */
export const MIN_PASSWORD = 12;

/** How long this password is, counted the way the reader counted it. */
export function passwordLength(password: string): number {
  return [...password].length;
}

/** Whether the password clears the floor. Empty is not "too short" — it is
 *  unanswered, and a form that scolds a field nobody has typed in yet is
 *  reporting on itself rather than on the reader. */
export function isTooShort(password: string): boolean {
  return password.length > 0 && passwordLength(password) < MIN_PASSWORD;
}
