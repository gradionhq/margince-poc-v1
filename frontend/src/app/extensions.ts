import {
  type ExtensionDescriptor,
  type ExtensionVerbDescriptor,
  extensions,
} from "@composition/extensions";
import type { ExtensionRbacObject } from "./capability";

// What the SPA knows about the units an installation composed.
//
// The registry arrives through the "@composition/extensions" alias, which is
// the TypeScript mirror of GOWORK: the DEFAULT lane resolves the committed
// empty-tree stub at src/composition/extensions.gen.ts, and a composed lane
// (tsconfig.composed.json + MARGINCE_COMPOSITION_FRONTEND) repoints the alias
// at build/composition/frontend/. One program, two registries — never two
// programs, and never a committed file regenerated from a composed tree, which
// would destroy the empty-tree byte-identity the composition gates prove.
//
// Everything below is a pure function of that registry and takes it as an
// argument, defaulting to the composed one. That is not test scaffolding: the
// vanilla registry is empty by construction, so a lookup that read the module
// binding directly could only ever be exercised on its miss path.

export type { ExtensionDescriptor, ExtensionVerbDescriptor };

/**
 * The route segment every unit surface lives under: `#/ext/<name>`.
 *
 * Spelled once and exported, because App.tsx's switch and any caller building
 * a link have to agree — a registry keyed under a screen name the router never
 * dispatches on resolves nothing, however correct its lookup is. `ext_` is the
 * namespace token everywhere else in the tier (tables, roles, job kinds,
 * `/v1/ext/<name>` routes); this is its hash-route spelling.
 */
export const EXTENSION_SCREEN = "ext";

/** The composed registry itself, for a caller that needs the whole set. */
export const composedExtensions: readonly ExtensionDescriptor[] = extensions;

/**
 * The unit `name` addresses, or null when this installation composed none.
 *
 * Null rather than a thrown error or an empty descriptor: an unknown unit is
 * an ordinary state of a hand-typed or bookmarked hash, and the vanilla tree —
 * where EVERY unit route is unknown — is the default one. The caller renders
 * the not-found surface; nothing here decides that.
 *
 * The match is exact. A unit name is a directory name, and the Postgres role,
 * schema objects and route prefix derived from it are all lowercase, so an
 * uppercase spelling addresses a unit that does not exist rather than the same
 * one written differently.
 */
export function findExtension(
  name: string | undefined,
  registry: readonly ExtensionDescriptor[] = extensions,
): ExtensionDescriptor | null {
  if (!name) {
    return null;
  }
  return registry.find((unit) => unit.name === name) ?? null;
}

/**
 * Whether `value` is an object an extension unit could have registered.
 *
 * A type predicate, not a cast: the check and the narrowing are the same
 * expression, so there is no way for the runtime test and the compile-time
 * claim to drift. The `ext_` prefix alone is deliberately all it asserts — the
 * server owns the full grammar (`policy.Object.Validate`, and the derivation
 * that refuses two units collapsing onto one name), and a second, weaker copy
 * of it here would eventually disagree with the authority.
 */
export function isExtensionRbacObject(
  value: string,
): value is ExtensionRbacObject {
  return value.startsWith("ext_") && value.length > "ext_".length;
}

/**
 * The capability object a unit screen gates on for `verb`, or null.
 *
 * Null covers both of today's honest cases: a verb that declares no object
 * (neither in-tree unit owns records, so the descriptor carries ""), and a
 * verb whose declared object is not in the extension namespace. The second is
 * the load-bearing one — handing a CORE object out of an extension descriptor
 * would let a unit's screen read a grant it never asked an operator for, and
 * the value would typecheck against `useCan` because core objects are in the
 * union too.
 */
export function extensionRbacObject(
  verb: ExtensionVerbDescriptor,
): ExtensionRbacObject | null {
  return isExtensionRbacObject(verb.rbacObject) ? verb.rbacObject : null;
}
