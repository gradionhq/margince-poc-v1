// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { LucideIcon } from "lucide-react";
import type { MessageKey } from "../i18n/en";
import { isScreen, type Route, routeHash } from "./router";

// The sidebar is a STACK of navigation levels, not one rail with a special case
// bolted on for Settings. Level one is the rail's own destinations; a screen
// that owns a set of sub-surfaces publishes them as a SECTION, and an entry
// inside a section may publish children of its own. All of it is data: the rail
// renders whatever depth the data describes, so a third level costs a
// `children` array rather than a redesign.
//
// What is deliberately NOT here is who may see an entry. Visibility is
// grant-dependent and belongs to the screen that owns the entries — a section
// arrives already filtered and with its active entry resolved, which is how the
// shell stays ignorant of grants and this module stays free of any screen.

export type NavLevelEntry = {
  // The route segment this entry addresses at its own depth: `#/settings/admin/privacy`
  // is the `privacy` entry of the section the `settings` screen publishes, one
  // segment deeper than the personal entries — see `prefix` below.
  id: string;
  // Route segments between the level's own path and this entry's `id`. Absent
  // for every entry that sits directly under its level, which is most of them.
  //
  // It exists because one level can address two depths: the settings level
  // lists the personal entries at `#/settings/voice` and the admin ones at
  // `#/settings/admin/privacy`, under one heading pair in one panel. The
  // alternative was a second nav LEVEL for the admin group, which would make a
  // reader drill through a row to reach a list the panel can already show, and
  // the `id` stays the entry's identity either way — so `activeId` matching is
  // unaffected by how deep the entry lives.
  prefix?: readonly string[];
  labelKey: MessageKey;
  icon: LucideIcon;
  // The level this entry opens. Grouping is possible at every depth, so the
  // children are a flat list only until one needs headings.
  children?: readonly NavLevelEntry[];
};

export type NavLevelGroup = {
  headingKey?: MessageKey;
  items: readonly NavLevelEntry[];
};

// What a screen hands the shell. `activeId` is the screen's OWN answer —
// fallbacks for an unknown or forbidden segment included — so the rail and the
// content beside it can never disagree about which entry is current.
export type NavSection = {
  screen: string;
  titleKey: MessageKey;
  groups: readonly NavLevelGroup[];
  activeId?: string;
};

// The attention counts the rail badges. They ride the level rather than being
// read from module scope inside a row, so a deeper level that grows badges one
// day answers the question in its own data instead of in another branch.
export type NavCounts = Partial<Record<string, number>>;

// One level as the rail renders it, with everything a row needs resolved. A
// level does not know its own depth: `path` is the route prefix its entries hang
// off, which is the only thing depth changes.
export type NavTrailLevel = {
  // Absent on the primary level, which the navigation landmark already names.
  // Present, it prints the level's own heading and pushes the group labels a
  // heading level down.
  titleKey?: MessageKey;
  groups: readonly NavLevelGroup[];
  activeId?: string;
  // Whether the active row IS the page, or only the section the page sits in.
  // A record route makes its list's row active while the page is the record —
  // and the trail in the top bar is what names that page, and claims it. Two
  // elements claiming `aria-current="page"` for different things is worse than
  // one claiming a little less, so a row that is only an ancestor says
  // `aria-current="true"` instead: current in this set, not the page.
  //
  // On the PRIMARY level the answer arrives with the level, because whether a
  // route's segments reach a page below the screen is a question about the
  // screens, and this module deliberately knows none of them (app/nav.ts, which
  // owns the destinations, answers it there). A level built below the primary
  // one is a section the page sits in and says nothing here.
  ancestor?: boolean;
  path: readonly string[];
  badgeIds?: ReadonlySet<string>;
  barIds?: ReadonlySet<string>;
};

// The route an entry of `path` addresses. The router parses four segments, so a
// level can be addressed three deep below the screen and no deeper — a fifth
// level would have to arrive with the route that can name it.
export function navLevelRoute(path: readonly string[], id: string): Route {
  const segments = [...path, id];
  return {
    // A level's path is strings by the time a row is a link, so its first
    // segment is checked here exactly as a typed hash's is: a level rooted at no
    // screen this app answers addresses the not-found page rather than minting a
    // link to nowhere.
    screen: isScreen(segments[0]) ? segments[0] : "not-found",
    id: segments[1],
    id2: segments[2],
    id3: segments[3],
  };
}

// The same address as a link target. A row is a link and a walk between levels
// is a navigation, so both spell the address once, here.
export function navLevelHref(path: readonly string[], id: string): string {
  return routeHash(navLevelRoute(path, id));
}

/**
 * Where one ENTRY of a level lives — its `prefix` included.
 *
 * Every row a reader can press goes through this rather than through
 * `navLevelHref` with a bare id: an entry that sits deeper than its level would
 * otherwise be linked to the level's own depth, and the link would land on
 * whatever answers that shorter address.
 */
export function navEntryRoute(
  path: readonly string[],
  entry: NavLevelEntry,
): Route {
  return navLevelRoute([...path, ...(entry.prefix ?? [])], entry.id);
}

export function navEntryHref(
  path: readonly string[],
  entry: NavLevelEntry,
): string {
  return routeHash(navEntryRoute(path, entry));
}

function activeEntry(level: NavTrailLevel): NavLevelEntry | undefined {
  for (const group of level.groups) {
    const found = group.items.find((item) => item.id === level.activeId);
    if (found) {
      return found;
    }
  }
  return undefined;
}

/**
 * The levels this route reaches, outermost first.
 *
 * `top` is injected rather than imported so this module owes nothing to the
 * canonical destination list (app/nav.ts owns that, and re-exports the wrapper
 * that supplies it). The array is the whole depth contract: the renderer walks
 * it and never counts levels itself.
 */
export function navTrail(
  top: NavTrailLevel,
  route: Route,
  // Which of the top level's rows the route makes current. For every destination
  // the PRODUCT owns this is the route's screen; the caller that knows better
  // passes something else (app/nav.ts: a composed unit's route has screen `ext`
  // and no row of its own). An id no rendered row carries simply marks nothing —
  // on those routes the trail in the top bar is what says where the reader is.
  activeId: string,
  section?: NavSection,
): readonly NavTrailLevel[] {
  // `ancestor` rides in on `top`: whether this route's segments reach a page
  // below the screen depends on what the screen does with them, which is
  // knowledge this module does not have and app/nav.ts does.
  const trail: NavTrailLevel[] = [{ ...top, activeId }];
  // Compared against the ROW the route makes current, not the route's raw
  // screen. They are the same string for every destination the product owns, and
  // they differ for exactly one case: a composed unit routes as
  // `{screen: "ext"}` and is reached from Settings, which is what `activeRowFor`
  // has always answered for it. On the raw screen the level was dropped, so
  // following "Open" from a settings card replaced the whole sidebar while the
  // URL and the trail still said Settings.
  if (!section || section.screen !== activeId) {
    return trail;
  }
  // The segments below the screen, in order: each selects an entry of the level
  // the one before it opened.
  const segments = [route.id, route.id2, route.id3];
  let level: NavTrailLevel = {
    titleKey: section.titleKey,
    groups: section.groups,
    activeId: section.activeId ?? route.id,
    // The SECTION's screen, which every row in it points under. Identical to the
    // route's for a route of that screen, and the only correct one for a unit
    // route standing in this level: rows built from `ext` would link to
    // addresses that resolve to nothing.
    path: [section.screen],
  };
  trail.push(level);
  for (let depth = 1; depth < segments.length; depth += 1) {
    const active = activeEntry(level);
    const children = active?.children;
    if (!active || !children || children.length === 0) {
      break;
    }
    level = {
      // A child level is named by the entry that opened it — the reader drilled
      // in through that word, so it is the word that says where they are.
      titleKey: active.labelKey,
      groups: [{ items: children }],
      activeId: segments[depth],
      path: [...level.path, active.id],
    };
    trail.push(level);
  }
  return trail;
}
