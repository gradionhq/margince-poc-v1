import type { components } from "../api/schema";
import type { RbacAction, RbacObject } from "./capability";

// The /me body a test serves, built from the grants under test rather than
// from a role name.
//
// Tests used to stub `roles: ["admin"]` and rely on the screen inferring
// capability from it. That made every assertion a statement about the seeded
// matrix — so a screen wired to the WRONG object still passed, because admin
// holds everything and one wrong grant looks exactly like the right one.
//
// Naming the grants makes the binding testable: a fixture that allows
// `automation:update` and nothing else proves the screen asks for exactly
// that. Divergent fixtures (one object granted, its neighbour not; one action
// granted, its sibling not) are the point, not an edge case.

type MeResponse = components["schemas"]["MeResponse"];
type RbacObjectGrant = components["schemas"]["RbacObjectGrant"];

const NO_GRANT: RbacObjectGrant = {
  create: false,
  read: false,
  update: false,
  delete: false,
};

/** Every action on an object, for the cases that genuinely need all four. */
export const ALL: readonly RbacAction[] = [
  "create",
  "read",
  "update",
  "delete",
];

export type GrantSpec = Partial<Record<RbacObject, readonly RbacAction[]>>;

/**
 * A /me response granting exactly `allow` and nothing else.
 *
 * Objects absent from `allow` are omitted entirely rather than written as an
 * all-false grant — that is what the server does for an object a role was
 * never granted, so a client that mistakes an absent key for an unrestricted
 * one fails here rather than in production.
 */
export function meFixture({
  roles = ["admin"],
  seat = "full",
  allow = {},
}: {
  roles?: string[];
  seat?: "full" | "read";
  allow?: GrantSpec;
} = {}): MeResponse {
  const objects: Record<string, RbacObjectGrant> = {};
  for (const [object, actions] of Object.entries(allow)) {
    objects[object] = (actions ?? []).reduce<RbacObjectGrant>(
      (grant, action) => ({ ...grant, [action]: true }),
      NO_GRANT,
    );
  }
  const me: MeResponse = {
    user: {
      id: "00000000-0000-4000-8000-000000000001",
      workspace_id: "00000000-0000-4000-8000-000000000002",
      email: "test@example.test",
      display_name: "Test User",
      timezone: "Europe/Berlin",
      status: "active",
      is_agent: false,
    },
    roles,
    teams: [],
    workspace_name: "Test Workspace",
    non_production: false,
    authorization: { seat_type: seat, objects },
  };
  return me;
}
