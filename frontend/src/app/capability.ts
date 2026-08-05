import type { components } from "../api/schema";
import { useMe } from "../screens/common";

// Scoping an affordance to the permission it actually needs.
//
// The four predicates this replaces asked which ROLE the user held and inferred
// the grant from the seeded matrix. That inference is only true of a freshly
// bootstrapped installation: the server reads live `role.permissions` at
// authentication, so on any workspace whose stored grants have drifted — an
// object added after bootstrap, an operator-edited role — the client and the
// server disagree, and the client renders a button the server then refuses.
//
// So the answer comes from the server (GET /me carries the merged grants it
// computed) and only the VOCABULARY comes from the contract. `RbacObject` and
// `RbacAction` are generated from crm.yaml's enums, so a misspelled object or
// verb is a TypeScript error rather than a check that compiles and silently
// denies forever.
//
// This is UX honesty, never enforcement: the server's auth.Require is the
// authority on every call, and a client that gets this wrong shows the wrong
// button, not the wrong data.

export type RbacObject = components["schemas"]["RbacObject"];
export type RbacAction = components["schemas"]["RbacAction"];

/**
 * Whether the current principal holds `action` on `object`.
 *
 * Fails closed on every uncertainty — /me still loading, a server too old to
 * send the snapshot, an object the principal holds no grant on. An unknown
 * object resolves to the zero grant on the server too, so a missing key is a
 * denial rather than a gap to fill in optimistically.
 *
 * It answers the OBJECT-RBAC question only. It deliberately does not fold in
 * the licensing seat ceiling, which the server clamps on the HTTP METHOD, not
 * on the RBAC action — the two diverge in both directions, and a route's RBAC
 * action does not tell you which method it is reached by. `GET /ai/usage`
 * gates on `automation:update`, so folding a seat check into an `update`
 * answer would hide a page a read seat may genuinely see.
 */
export function useCan(object: RbacObject, action: RbacAction): boolean {
  const me = useMe();
  // Two optional steps, both load-bearing. `objects` is an index signature, so
  // TypeScript types a miss as PRESENT — only the chain makes an absent grant
  // deny at runtime; and the chain must cover `objects` itself, or a snapshot
  // that arrived without it would throw in render rather than deny.
  const grant = me.data?.authorization?.objects?.[object];
  return grant?.[action] ?? false;
}

/**
 * Whether the licensing seat permits mutating at all (A62/ADR-0047).
 *
 * The server clamps this on the HTTP METHOD, before RBAC, so a read seat may
 * read everything its roles grant and write none of it. Only an explicit
 * `full` passes: an absent, unreadable or unrecognized seat denies, so a
 * snapshot that lost the field cannot buy the ability to mutate.
 */
export function useCanMutate(): boolean {
  return useMe().data?.authorization?.seat_type === "full";
}

/**
 * Whether the principal holds ANY write verb on `object` — create, update or
 * delete.
 *
 * For a surface that exists to AUTHOR an object rather than to issue one
 * specific request: a nav entry into a configuration page, a section heading
 * over a set of editors. Which write verb a role holds varies by object — a
 * rep creates and updates a product but never deletes one — so a single-verb
 * question would hide an authoring surface from a principal who plainly uses
 * it, and the union is the honest one.
 *
 * Like `useCan`, and unlike `useCanWrite`, it leaves the seat ceiling out. A
 * read seat still READS the page behind such an entry, so folding the seat in
 * would strand a reader on a fallback screen while protecting nothing the
 * server does not already enforce on the write itself.
 */
export function useHoldsWriteGrant(object: RbacObject): boolean {
  // Three unconditional calls, then the composition. Writing the `||` around
  // the calls would make the number of hooks a render runs depend on the
  // answer, which React forbids.
  const create = useCan(object, "create");
  const update = useCan(object, "update");
  const remove = useCan(object, "delete");
  return create || update || remove;
}

/**
 * Both axes, for a control that issues a MUTATING request — the common case.
 *
 * Use `useCan` alone only where the control issues a GET whose RBAC action
 * happens to be a write verb (`GET /ai/usage` gates on `automation:update`;
 * the overlay user-map listing gates on `overlay_connection:update`). Folding
 * the seat into those would hide a page a read seat may genuinely see.
 */
export function useCanWrite(object: RbacObject, action: RbacAction): boolean {
  const granted = useCan(object, action);
  const mutable = useCanMutate();
  return granted && mutable;
}
