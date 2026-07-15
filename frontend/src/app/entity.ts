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
