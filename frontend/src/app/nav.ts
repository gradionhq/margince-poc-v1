import type { ExtensionDescriptor } from "@composition/extensions";
import {
  BarChart3,
  Blocks,
  Building2,
  CheckSquare,
  Home,
  type LucideIcon,
  Merge,
  ShieldCheck,
  Sparkles,
  Target,
  UserPlus,
  Users,
} from "lucide-react";
import type { MessageKey } from "../i18n/en";
import { composedExtensions, EXTENSION_SCREEN } from "./extensions";
import type { Route } from "./router";
import {
  type NavLevelGroup,
  type NavSection,
  type NavTrailLevel,
  navTrail,
} from "./subnav";

// The level registry lives beside this list and is reached through it: a caller
// asking where the sidebar can go has one module to import.
export type {
  NavCounts,
  NavLevelEntry,
  NavLevelGroup,
  NavSection,
  NavTrailLevel,
} from "./subnav";
export { navLevelHref, navLevelRoute } from "./subnav";

// The primary nav. Order is normative and shell.test.tsx pins it. Home stands
// alone above three labeled groups; the groups are the expanded sidebar's own
// structure and collapse to hairline rules at 64px, so the collapsed rail is the
// flat list WDS-NAV-1 describes.
//
// It carries ten rows against upstream's own ten, but not the SAME ten: Duplicates
// is a destination here and Automations is not. Automations is set-and-forget
// configuration and now lives inside Settings → AI, which is where the product
// already offered a second door to it; the dedupe queue is work somebody has to
// get through, and it had no address outside a home digest card. Both divergences
// are the UI's to make and are on the founder's back-fill list.
//
// `screen` is the route id and never changes with a label: `deals` presents as
// Pipeline (it routes to the pipeline surface) and `inbox` presents as
// Approvals (it is a governance surface, not a mailbox).
export type NavItem = {
  screen: string;
  labelKey: MessageKey;
  icon: LucideIcon;
};

export type NavGroup = {
  headingKey?: MessageKey;
  items: readonly NavItem[];
};

export const NAV_GROUPS: readonly NavGroup[] = [
  { items: [{ screen: "home", labelKey: "nav.home", icon: Home }] },
  {
    headingKey: "nav.group.records",
    items: [
      { screen: "contacts", labelKey: "nav.contacts", icon: Users },
      { screen: "companies", labelKey: "nav.companies", icon: Building2 },
      { screen: "leads", labelKey: "nav.leads", icon: UserPlus },
      // Merging two records that are one is WORK on the records above it, not
      // configuration — it was reachable only from a home digest card, which is
      // no address at all for a queue somebody has to work through. It keeps that
      // card; this is the destination the card now points into.
      { screen: "dedupe", labelKey: "nav.dedupe", icon: Merge },
    ],
  },
  {
    headingKey: "nav.group.work",
    items: [
      { screen: "deals", labelKey: "nav.deals", icon: Target },
      { screen: "tasks", labelKey: "nav.tasks", icon: CheckSquare },
      // ShieldCheck, not another check-in-square: Approvals sits directly under
      // Tasks and a near-identical glyph makes the pair unreadable at 20px.
      { screen: "inbox", labelKey: "nav.inbox", icon: ShieldCheck },
    ],
  },
  {
    headingKey: "nav.group.intelligence",
    items: [
      { screen: "reports", labelKey: "nav.reports", icon: BarChart3 },
      { screen: "ai", labelKey: "nav.ai", icon: Sparkles },
    ],
  },
];

export const NAV: readonly NavItem[] = NAV_GROUPS.flatMap(
  (group) => group.items,
);

// A badge counts only what wants a human's attention (Tasks due, Approvals
// waiting). Ambient totals are deliberately absent: the list endpoints are
// keyset-paginated and are not known to return one, and a decorative count
// contradicts the badge rule.
export const BADGE_SCREENS: ReadonlySet<string> = new Set(["tasks", "inbox"]);

// At phone width the sidebar becomes a bottom bar, which fits four thumb-sized
// destinations plus More — ten would need horizontal scrolling, and a nav you
// have to scroll is a nav you cannot see. Approvals is non-negotiable here: the
// 390px approval path is required for V1.
export const MOBILE_PRIMARY: ReadonlySet<string> = new Set([
  "home",
  "contacts",
  "deals",
  "inbox",
]);

// Documented rail-less exceptions (AC-shell layout exception): onboarding,
// the public booking page, the extension client surfaces, and the OAuth
// consent screen — a human lending an agent their authority reads it apart
// from the rest of the app, not framed inside it.
export const RAIL_LESS_SCREENS: ReadonlySet<string> = new Set([
  "onboarding",
  "book",
  "client",
  "preferences",
  "oauth-consent",
]);

// The destinations as a LEVEL, so the renderer that walks a trail treats level
// one exactly like any level below it — same rows, same tooltips, same active
// rule, one place where a nav row is spelled. The badge and phone-bar sets ride
// the level for the same reason: a row asks its level which ids badge rather
// than reaching for a module-scope set of its own.
//
// It prints no heading: the navigation landmark names it, and its GROUPS are the
// level-2 headings the sidebar promises.
function primaryLevel(units: readonly ExtensionDescriptor[]): NavTrailLevel {
  const groups: NavLevelGroup[] = NAV_GROUPS.map((group) => ({
    headingKey: group.headingKey,
    items: group.items.map((item) => ({
      id: item.screen,
      labelKey: item.labelKey,
      icon: item.icon,
    })),
  }));
  if (units.length > 0) {
    groups.push(unitsGroup(units));
  }
  return {
    groups,
    path: [],
    badgeIds: BADGE_SCREENS,
    barIds: MOBILE_PRIMARY,
  };
}

// The composed units, as the LAST group and only when there are any.
//
// Last because the ten above are normative and their order is pinned: an
// installation's own surfaces sit after the product's rather than interleaved
// with them. Absent when the set is empty, which is the vanilla tree — a
// heading over nothing is a promise the installation did not make, and it is
// why the pinned Records / Work / Intelligence order needed no revising here.
//
// One row per UNIT, not per operation: a unit publishes several governed verbs
// and they are one destination, its screen. The label is the unit's own name,
// carried as `label` because it is the INSTALLATION's text and has no
// translation — nav.units.entry is the fallback a renderer would otherwise have
// to invent, and it is never what a composed row shows.
//
// No badge and no phone-bar slot: BADGE_SCREENS and MOBILE_PRIMARY are the
// product's judgement about what wants a person's attention, and that is not a
// call this layer can make for a surface it did not write.
// Which primary row a route makes current. It is the route's screen for every
// destination the product owns, and `ext/<unit>` for a unit's — a unit screen
// routes as `{screen: "ext", id: "<unit>"}`, so a level whose ids are screens
// alone marks NOTHING current on the only routes this group exists for.
function activeRowFor(route: Route): string {
  return route.screen === EXTENSION_SCREEN && route.id
    ? `${EXTENSION_SCREEN}/${route.id}`
    : route.screen;
}

function unitsGroup(units: readonly ExtensionDescriptor[]): NavLevelGroup {
  return {
    headingKey: "nav.group.units",
    items: units.map((unit) => ({
      id: `${EXTENSION_SCREEN}/${unit.name}`,
      labelKey: "nav.units.entry",
      label: unit.name,
      icon: Blocks,
    })),
  };
}

// The levels the sidebar shows for a route: the destinations, then whatever the
// screen on that route published under them.
export function railTrail(
  route: Route,
  section?: NavSection,
  units: readonly ExtensionDescriptor[] = composedExtensions,
): readonly NavTrailLevel[] {
  return navTrail(primaryLevel(units), route, section, activeRowFor(route));
}
