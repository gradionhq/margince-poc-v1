/**
 * The release this bundle was built from, and the one rule for comparing it.
 *
 * WHY THE SPA CARES. A customer pulls each role image by tag, and the tag
 * resolver answers each pull separately. Two pulls are two requests, so a
 * publish landing between them hands back a web tier and an api from different
 * releases; the OCI distribution protocol cannot express "these manifests or
 * none", so the registry cannot refuse it at the pull. The api and the worker
 * settle it between themselves against the database they share. This tier
 * cannot: it is a static bundle in a browser, so all it can do is ask the api
 * what release it is and refuse to render against a different one.
 *
 * It also covers the ordinary, much more frequent version of the same problem:
 * a tab left open across a deploy is running last week's bundle against this
 * week's api, and every request it makes is a guess about a contract that has
 * moved.
 *
 * THIS MIRRORS internal/shared/buildinfo ON THE GO SIDE — same "dev" sentinel,
 * same "unknown disables the comparison" rule — because the two halves have to
 * agree about what counts as a difference. There is no way to share the code
 * across the language boundary, so the obligation is to keep the two files
 * saying the same thing.
 */

// Replaced at build time by vite.config.ts's `define`. Declared here rather than
// in an ambient .d.ts so the one module that reads it also documents it.
declare const __MARGINCE_RELEASE_VERSION__: string;

/**
 * SPA_RELEASE is the release this bundle was published as. Empty in every local
 * build, which is what UNKNOWN answers for.
 */
export const SPA_RELEASE: string = __MARGINCE_RELEASE_VERSION__;

/**
 * UNKNOWN mirrors buildinfo.Unknown: the value a plain `docker build` stamps
 * when no release was named. Empty means the same thing — nothing stamped it at
 * all — and both must read as "this build does not know".
 */
const UNKNOWN = "dev";

/**
 * comparableRelease reports whether a release version is one worth comparing.
 * Absent, empty and "dev" are all "this build does not know", and a difference
 * from any of them means nothing.
 */
export function comparableRelease(release: string | undefined): boolean {
  return release !== undefined && release !== "" && release !== UNKNOWN;
}

/**
 * releaseSkew reports whether two releases are known AND different, which is the
 * only combination that says anything. Everything else — either side unknown, or
 * both known and equal — is false.
 *
 * FALSE IS THE ANSWER FOR EVERY UNCERTAINTY, and that direction is deliberate.
 * This function gates whether the app renders at all, so a wrong `true` takes a
 * healthy installation down while a wrong `false` leaves it exactly where it was
 * before this guard existed.
 */
export function releaseSkew(
  mine: string | undefined,
  theirs: string | undefined,
): boolean {
  if (!comparableRelease(mine) || !comparableRelease(theirs)) {
    return false;
  }
  return mine !== theirs;
}
