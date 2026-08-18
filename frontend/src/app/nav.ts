import {
  BarChart3,
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
import { EXTENSION_SCREEN } from "./extensions";
import type { Route } from "./router";
import { type NavSection, type NavTrailLevel, navTrail } from "./subnav";

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

// Which RECORD screens keep the reading column instead of taking the width they
// are given. This is the one place that decision lives, because it is a
// judgement per surface and it gets revised by opening the page and looking:
// move a screen out of this set and it goes full width, put one in and it is
// capped. Settings is always capped and is not listed here — it is a whole
// section, not a record.
//
// The two that are here read DOWN rather than across: a rail of facts beside
// prose, where a measured line length is the point and a fact a monitor away
// from its label is worse, not wider. A list, a board or a report is scanned
// ACROSS, and the cap only ever pushed columns off the right edge of a wide
// display.
//
// Keyed on the screen, applied only when the route carries an id: `#/companies`
// is the list and belongs to the other family, `#/companies/<id>` is the record.
export const GRIDDED_RECORD_SCREENS: ReadonlySet<string> = new Set([
  "companies",
  "contacts",
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
//
// It carries the product's OWN destinations and nothing else. A composed unit
// had a group of its own here once; it does not any more. An installation
// enabling a unit is not the same as the product growing an eleventh
// destination, and the rail is the one surface where that distinction is
// visible to every person who uses the app.
//
// The unit's screen is still reachable at `#/ext/<unit>` — what changed is
// where it is OFFERED: Settings, on the page that already holds the credential
// the unit is configured with (see screens/extension-units.tsx). That is also
// what makes the offer honest about permission, because the two settings pages
// are already split by whose thing each surface is.
function primaryLevel(): NavTrailLevel {
  return {
    groups: NAV_GROUPS.map((group) => ({
      headingKey: group.headingKey,
      items: group.items.map((item) => ({
        id: item.screen,
        labelKey: item.labelKey,
        icon: item.icon,
      })),
    })),
    path: [],
    badgeIds: BADGE_SCREENS,
    barIds: MOBILE_PRIMARY,
  };
}

// Which primary row a route makes current. It is the route's screen for every
// destination the product owns, and `settings` for a unit's — a unit screen
// routes as `{screen: "ext", id: "<unit>"}` and has no row of its own, so
// without this the rail would mark NOTHING current on it and the page would
// read as if it sat outside the app. Settings is where the reader came from
// and where the unit is listed, so it is the honest answer rather than a
// convenience.
function activeRowFor(route: Route): string {
  return route.screen === EXTENSION_SCREEN ? "settings" : route.screen;
}

// The levels the sidebar shows for a route: the destinations, then whatever the
// screen on that route published under them.
export function railTrail(
  route: Route,
  section?: NavSection,
): readonly NavTrailLevel[] {
  return navTrail(primaryLevel(), route, section, activeRowFor(route));
}
