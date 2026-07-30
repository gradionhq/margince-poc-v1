import type { Route } from "./router";

// The one place the app's record kinds are enumerated. The history endpoints,
// EntityRef, and LogActivity all speak this vocabulary; before this registry
// each kept its own person|organization|deal union (all missing lead).
// `activity` is intentionally absent: it is the timeline, not a 360 record.
export type EntityKind = "person" | "organization" | "deal" | "lead";

export const ENTITY_KINDS = [
  "person",
  "organization",
  "deal",
  "lead",
] as const satisfies readonly EntityKind[];

export type EntityDescriptor = {
  route: (id: string) => Route;
};

export const ENTITY: Record<EntityKind, EntityDescriptor> = {
  person: {
    route: (id) => ({ screen: "contacts", id }),
  },
  organization: {
    route: (id) => ({ screen: "companies", id }),
  },
  deal: {
    route: (id) => ({ screen: "deals", id }),
  },
  lead: {
    route: (id) => ({ screen: "leads", id }),
  },
};

// The reverse of ENTITY[kind].route: which record kind a screen's `id` segment
// names, so the breadcrumb can show "Anna Weber" instead of an opaque id. It is
// DERIVED from the routes rather than restated, because a hand-written copy goes
// stale silently — a new kind whose screen is missing here degrades the crumb to
// a raw uuid with nothing to catch it. A screen absent from the routes has no
// record segment worth resolving and is absent here too.
export const SCREEN_ENTITY: Readonly<Record<string, EntityKind>> =
  Object.fromEntries(
    ENTITY_KINDS.map((kind) => [ENTITY[kind].route("").screen, kind]),
  );
