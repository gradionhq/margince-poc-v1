// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { LucideIcon } from "lucide-react";
import type { MessageKey } from "../i18n/en";
import { type Route, routeHash } from "./router";

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
  // The route segment this entry addresses at its own depth: `#/settings/audit`
  // is the `audit` entry of the section the `settings` screen publishes.
  id: string;
  labelKey: MessageKey;
  // label is what an entry the PRODUCT did not name is called: a composed
  // unit's title is the installation's text, so it has no message key and no
  // translation. When present it wins over labelKey, which such an entry sets
  // to the generic fallback a caller would otherwise have to invent.
  label?: string;
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
    screen: segments[0],
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
  section?: NavSection,
  // Which of the top level's rows the route makes current. It defaults to the
  // route's screen, which is what a primary entry's id is for every
  // destination the PRODUCT owns — and is passed explicitly by the caller that
  // knows better: a composed unit's row is `ext/<unit>` while its route's
  // screen is `ext`, so deriving it here would mark nothing current on every
  // unit route.
  activeId: string = route.screen,
): readonly NavTrailLevel[] {
  const trail: NavTrailLevel[] = [{ ...top, activeId }];
  if (!section || section.screen !== route.screen) {
    return trail;
  }
  // The segments below the screen, in order: each selects an entry of the level
  // the one before it opened.
  const segments = [route.id, route.id2, route.id3];
  let level: NavTrailLevel = {
    titleKey: section.titleKey,
    groups: section.groups,
    activeId: section.activeId ?? route.id,
    path: [route.screen],
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
