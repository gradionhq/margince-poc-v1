import {
  BarChart3,
  Bell,
  Briefcase,
  Building2,
  CheckSquare,
  Home,
  Kanban,
  ListFilter,
  type LucideIcon,
  Merge,
  Sparkles,
  UserPlus,
  Users,
} from "lucide-react";
import type { MessageKey } from "../i18n/en";
import { SCREEN_ENTITY } from "./entity";
import { EXTENSION_SCREEN } from "./extensions";
import type { Route, Screen } from "./router";
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
export {
  navEntryHref,
  navEntryRoute,
  navLevelHref,
  navLevelRoute,
} from "./subnav";

// The primary nav. Order is normative and rail.test.tsx pins it. Home stands
// alone above three labeled groups; the groups are the expanded sidebar's own
// structure and collapse to hairline rules at 64px, so the collapsed rail is the
// flat list WDS-NAV-1 describes.
//
// It carries twelve rows against upstream's own ten, and not as a superset:
// Duplicates, Filters & views and Projects are destinations here, Automations is not.
// Automations is set-and-forget configuration and now lives inside Settings →
// AI, which is where the product already offered a second door to it; the dedupe
// queue is work somebody has to get through, and it had no address outside a
// home digest card; the filter builder is a full authoring surface, and a screen
// this list does not name is a screen only a typed URL reaches. All three
// divergences are the UI's to make and are on the founder's back-fill list.
//
// `screen` is the route id and never changes with a label: `deals` presents as
// Pipeline (it routes to the pipeline surface) and `inbox` presents as
// Approvals (it is a governance surface, not a mailbox).
export type NavItem = {
  screen: Screen;
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
      // Slicing the records above it: a filter authored here becomes a dynamic
      // list, a saved view or an export, and each of those selects from the
      // record types this group names. So it belongs with them rather than under
      // Intelligence — nothing on this screen aggregates, it answers "which
      // records", which is the question the rows above it each answer with one
      // fixed set. It reuses the screen's own title rather than a `nav.*` label,
      // because a surface named twice gets renamed once.
      //
      // A funnel is the glyph every CRM draws a sales pipeline with, and
      // Pipeline is already a row on this list; a filtered list says what this
      // surface produces and cannot be read as a second door to the board.
      { screen: "filters", labelKey: "filters.title", icon: ListFilter },
    ],
  },
  {
    headingKey: "nav.group.work",
    items: [
      // The board, not a bullseye: this route opens a column per stage with the
      // deals standing in them, and `Target` drew a goal — which is what a quota
      // surface would be. A reader scanning five glyphs on a phone bar with no
      // labels under them has only the shape to go on.
      { screen: "deals", labelKey: "nav.deals", icon: Kanban },
      // The body of work a deal is about. It starts during the deal and
      // outlives close-won, so it sits beside the pipeline rather than under
      // it: a project in delivery has no deal column to stand in.
      { screen: "projects", labelKey: "nav.projects", icon: Briefcase },
      { screen: "tasks", labelKey: "nav.tasks", icon: CheckSquare },
      // The same bell the top bar rings. Approvals are the one queue that waits
      // on a PERSON, and the chrome reports it in three places at once — this
      // row, the strip's own affordance, and the phone bar; two glyphs for one
      // queue reads as two queues. It also keeps the pair under Tasks readable:
      // a shield-check beside a check-in-square was two ticks in two boxes at
      // 20px, and this is a different shape entirely.
      { screen: "inbox", labelKey: "nav.inbox", icon: Bell },
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
export const BADGE_SCREENS: ReadonlySet<Screen> = new Set(["tasks", "inbox"]);

// At phone width the sidebar becomes a bottom bar, which fits four thumb-sized
// destinations plus More — twelve would need horizontal scrolling, and a nav you
// have to scroll is a nav you cannot see. Approvals is non-negotiable here: the
// 390px approval path is required for V1.
export const MOBILE_PRIMARY: ReadonlySet<Screen> = new Set([
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
export const GRIDDED_RECORD_SCREENS: ReadonlySet<Screen> = new Set([
  "companies",
  "contacts",
]);

// Documented rail-less exceptions (AC-shell layout exception): onboarding,
// the public booking page, the extension client surfaces, and the OAuth
// consent screen — a human lending an agent their authority reads it apart
// from the rest of the app, not framed inside it.
export const RAIL_LESS_SCREENS: ReadonlySet<Screen> = new Set([
  "onboarding",
  "book",
  "client",
  "preferences",
  "room",
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
// enabling a unit is not the same as the product growing a twelfth
// destination, and the rail is the one surface where that distinction is
// visible to every person who uses the app.
//
// The unit's screen is still reachable at `#/ext/<unit>` — what changed is
// where it is OFFERED: Settings, on the page that already holds the credential
// the unit is configured with (see screens/extension-units.tsx). That is also
// what makes the offer honest about permission, because the two settings pages
// are already split by whose thing each surface is.
function primaryLevel(route: Route): NavTrailLevel {
  return {
    groups: NAV_GROUPS.map((group) => ({
      headingKey: group.headingKey,
      items: group.items.map((item) => ({
        id: item.screen,
        labelKey: item.labelKey,
        icon: item.icon,
      })),
    })),
    ancestor: opensARecord(route),
    path: [],
    badgeIds: BADGE_SCREENS,
    barIds: MOBILE_PRIMARY,
  };
}

// Whether the active row is only the SECTION the page sits in rather than the
// page itself — which is true exactly when the route opens a RECORD, because a
// record is the one thing a segment under a screen reaches that is a page of its
// own. The test is the top bar's own: `SCREEN_ENTITY` is what decides whether
// the trail up there ends in a record and claims to be the page, so deriving the
// row's answer from the same map is what keeps exactly one element claiming
// `aria-current="page"`. Asking `route.id !== undefined` instead read every
// segment as a page: `#/filters/companies` picks the object tab OF the filters
// page, and the row that leads there was demoted to an ancestor of a page that
// does not exist.
function opensARecord(route: Route): boolean {
  return route.id !== undefined && SCREEN_ENTITY[route.screen] !== undefined;
}

// Which primary row a route makes current. It is the route's screen for every
// destination the product owns, and `settings` for a unit's — a unit screen
// routes as `{screen: "ext", id: "<unit>"}` and has no row of its own. Nothing
// in the sidebar renders that id today: Settings left the rail for the account
// menu, so a unit route marks no row and the top bar's trail is what says where
// the reader is (`Settings / <unit>`). The mapping stays because it is the
// honest answer to the question — a unit IS reached from Settings — and because
// the level machinery asks it for every route, unit routes included.
function activeRowFor(route: Route): string {
  return route.screen === EXTENSION_SCREEN ? "settings" : route.screen;
}

// The levels the sidebar shows for a route: the destinations, then whatever the
// screen on that route published under them.
export function railTrail(
  route: Route,
  section?: NavSection,
): readonly NavTrailLevel[] {
  return navTrail(primaryLevel(route), route, activeRowFor(route), section);
}
