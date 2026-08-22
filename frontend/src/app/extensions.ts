import {
  type ExtensionDescriptor,
  type ExtensionVerbDescriptor,
  extensions,
} from "@composition/extensions";
import type { ComponentType } from "react";
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
 * The screens an installation composes, keyed by unit name.
 *
 * The type lives HERE, beside the descriptor registry, and not next to either
 * implementation of it: both lanes' modules import it, and one of those modules
 * (`src/screens/ext/`) is outside the vanilla TypeScript program entirely — a
 * type imported from there would pull the whole excluded directory back into
 * it.
 *
 * A component, not a rendered node: React needs a stable type to mount, and a
 * registry of pre-rendered elements would build every unit's screen on every
 * route change including the ones nobody navigated to.
 *
 * Unkeyed by a union of unit names on purpose — the composed set is an
 * installation's, not this program's, so the lookup is a miss like any other
 * and App.tsx already renders the honest fallback for one.
 */
export type ExtensionScreenRegistry = Readonly<
  Record<string, ComponentType | undefined>
>;

/**
 * The route segment every unit surface lives under: `#/ext/<name>`.
 *
 * Spelled once and exported, because App.tsx's switch and any caller building
 * a link have to agree — a registry keyed under a screen name the router never
 * dispatches on resolves nothing, however correct its lookup is. `ext_` is the
 * namespace token everywhere else in the tier (tables, roles, job kinds,
 * `/v1/ext/<name>` routes); this is its hash-route spelling.
 *
 * NOT REACHABLE FROM THE RAIL. `nav.ts` keeps `NAV_GROUPS` as the canonical
 * 12-item list whose order `rail.test.tsx` pins, and an installation cannot
 * add a thirteenth: enabling a unit is not the product growing a destination.
 * A unit is OFFERED in Settings instead, on the page that already holds the
 * kind of credential it is configured with — see `unitsForSecretScope` below
 * and `screens/extension-units.tsx`.
 *
 * `RAIL_LESS_SCREENS` was reviewed with it and is unchanged: a unit's screen
 * wants the rail like any other destination, and `nav.ts` marks Settings
 * current while it is open. What it does NOT get is a badge or a phone-bar
 * slot — both are the product's judgement about what deserves a person's
 * attention, and it cannot make one for a surface it did not write.
 */
export const EXTENSION_SCREEN = "ext";

/** The composed registry itself, for a caller that needs the whole set. */
export const composedExtensions: readonly ExtensionDescriptor[] = extensions;

/**
 * The two secret scopes a unit's settings entry can be placed by.
 *
 * The descriptor's own field is wider — it also carries "" for a unit that
 * declares no secret — and that third value is deliberately not something a
 * caller can ask for. Asking for it would mean asking for the units with
 * nothing to configure, which is not a page this product has.
 */
export type UnitSecretScope = "user" | "workspace";

/**
 * The units whose credential lives in `scope`, in the registry's order.
 *
 * This is the whole of the placement rule, and it is the manifest's own
 * vocabulary rather than a second one invented here: a `user` secret is one
 * member's own credential at a provider, so its unit belongs on the personal
 * Connections page; a `workspace` secret is the installation's, so its unit
 * belongs under Integrations beside the other credentials an operator curates.
 * Both settings pages already carry exactly that meaning, which is why the
 * declared scope is enough and no unit names a destination.
 *
 * A unit declaring no secret is in neither list, and that is not a gap: a
 * settings entry offers something to manage, and a unit holding no credential
 * has nothing to offer either page. Its screen stays reachable at
 * `#/ext/<unit>` for anyone holding the link.
 *
 * The registry is an argument, defaulting to the composed one, for the reason
 * every function in this file takes it: the vanilla registry is empty by
 * construction, so a filter reading the module binding directly could only
 * ever be exercised on its empty case.
 */
export function unitsForSecretScope(
  scope: UnitSecretScope,
  registry: readonly ExtensionDescriptor[] = extensions,
): readonly ExtensionDescriptor[] {
  return registry.filter((unit) => unit.secretScope === scope);
}

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
