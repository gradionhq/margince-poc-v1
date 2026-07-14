import type { LucideIcon } from "lucide-react";
import { Building2, Handshake, Sparkle, User } from "lucide-react";
import type { MessageKey } from "../i18n/en";
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
  labelKey: MessageKey;
  icon: LucideIcon;
  route: (id: string) => Route;
  displayNameField: "full_name" | "display_name" | "name";
};

export const ENTITY: Record<EntityKind, EntityDescriptor> = {
  person: {
    labelKey: "entity.person",
    icon: User,
    route: (id) => ({ screen: "contacts", id }),
    displayNameField: "full_name",
  },
  organization: {
    labelKey: "entity.organization",
    icon: Building2,
    route: (id) => ({ screen: "companies", id }),
    displayNameField: "display_name",
  },
  deal: {
    labelKey: "entity.deal",
    icon: Handshake,
    route: (id) => ({ screen: "deals", id }),
    displayNameField: "name",
  },
  lead: {
    labelKey: "entity.lead",
    icon: Sparkle,
    route: (id) => ({ screen: "leads", id }),
    displayNameField: "full_name",
  },
};
