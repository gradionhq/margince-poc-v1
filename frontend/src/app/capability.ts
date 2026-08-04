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
  // objects is an index signature, so TypeScript types a miss as present. The
  // optional chain is what actually makes an absent grant deny at runtime.
  const grant = me.data?.authorization?.objects[object];
  return grant?.[action] ?? false;
}
