// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { ChevronLeft } from "lucide-react";
import {
  createContext,
  type RefObject,
  useCallback,
  useContext,
  useEffect,
  useRef,
} from "react";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  type NavCounts,
  type NavLevelEntry,
  type NavLevelGroup,
  type NavSection,
  type NavTrailLevel,
  navEntryHref,
  navLevelRoute,
  railTrail,
} from "./nav";
import { navigate, type Route, routeHash } from "./router";

// ONE navigation level, whatever its depth. The rail's ten destinations and the
// entries of a section drilled into from them render through this, so the two
// can never drift into different rows — and a third level costs nothing but the
// data that describes it.
//
// Depth reaches this file only as data: a level carrying a title prints it and
// pushes its group labels a heading level down, and a level's entries address
// themselves from its `path`. Nothing here counts levels.

// The sidebar shows one tooltip at a time and keys it by the row's own ADDRESS,
// so two levels' rows cannot collide on a key — and the primary level's rows
// keep the screen id they have always been keyed by (its path is empty).
function navTipKey(level: NavTrailLevel, id: string): string {
  return [...level.path, id].join("/");
}

const BACK_TIP_KEY = "rail-level-back";

// Where a reader who never walked into the section is sent when they walk out
// of it: a deep link carries no origin, and an invented one would be a claim
// about where they had been.
const HOME: Route = { screen: "home" };

// What a walk between levels needs to remember, and both halves of it outlive
// the panel — because they have to. A section route swaps one rail component for
// the other (shell.tsx), so the rail is REMOUNTED in the middle of a walk: the
// panel that asks the question is never the panel that answers it. The shell
// holds this and hands it down; a rail rendered without one — a story, the
// component workbench — has only its own lifetime, and walks out to home.
type NavWalk = {
  // The last route that showed no level at all. Nothing about `#/settings/admin/privacy`
  // says which screen was open before it, so it is remembered as the reader
  // passes rather than reconstructed.
  origin: Route;
  // The ADDRESS whose level asked for focus, spent by the level that arrives at
  // it. An address rather than a flag: a walk changes the route, the route
  // changes on a `hashchange` that lands a task later, and any render in that gap
  // — a query settling, a menu closing — would let the level being LEFT spend a
  // bare flag on itself and focus a row it is about to unmount. Naming the target
  // means only the arriving level can spend it. Absent, nobody asked: merely
  // landing on a route that has a level must not pull focus off the page the
  // reader is reading.
  claimAt?: string;
};

const NavWalkMemory = createContext<RefObject<NavWalk> | undefined>(undefined);

export const NavWalkProvider = NavWalkMemory.Provider;

/**
 * The shell's memory of a walk between levels, held past the rail's lifetime.
 *
 * `remembers` is false on the routes that are not somewhere to walk back TO: a
 * section route is what a walk leaves, and a rail-less surface carries no
 * navigation to return into. No dependency list, because the route is parsed
 * fresh per render and would defeat one anyway.
 */
export function useNavWalk(
  route: Route,
  remembers: boolean,
): RefObject<NavWalk> {
  const walk = useRef<NavWalk>({ origin: HOME });
  useEffect(() => {
    if (remembers) {
      walk.current.origin = route;
    }
  });
  return walk;
}

// The address the way back leads to. Below the section's own level it is the
// entry the reader drilled through — that entry's own address is what names the
// shallower level. At the section's own level there is no address above it
// INSIDE the section, so the walk leaves: back to where the reader came in from.
function walkUpTarget(parent: NavTrailLevel | undefined, origin: Route): Route {
  if (!parent || parent.path.length === 0 || !parent.activeId) {
    return origin;
  }
  return navLevelRoute(parent.path, parent.activeId);
}

/**
 * The level the sidebar is showing, and the two ways a reader moves between
 * levels.
 *
 * The shown level is a pure function of the ROUTE. Both ways of moving change
 * the address, so the panel and the address can never disagree about which
 * level the reader is in — and the link back into a section is never the
 * address the reader is already standing on, which is what made a level the
 * panel had climbed out of unreachable.
 */
export function useNavLevel(
  route: Route,
  section: NavSection | undefined,
  container: RefObject<HTMLElement | null>,
  onNavigate: () => void,
) {
  const trail = railTrail(route, section);
  const depth = trail.length - 1;
  const shown = trail[depth];
  const parent = depth > 0 ? trail[depth - 1] : undefined;
  // With no shell above it the panel is all there is, so it keeps the walk's
  // memory itself — one lifetime, and no history before it.
  const own = useRef<NavWalk>({ origin: HOME });
  const walk = useContext(NavWalkMemory) ?? own;

  // Walking between levels replaces every row in the panel, and an unmounted
  // focus owner leaves the document focused on <body> — from where the next Tab
  // starts at the top of the page, having lost the sidebar it was standing in.
  // So the level that ARRIVES takes focus, and only when the walk was ASKED
  // for: merely landing on a route that has a level must not pull focus off the
  // page the reader is reading.
  //
  // Two moves ask, and each arms the claim itself: walking OUT, and standing on
  // a row that opens a deeper level. A direction with no arming site is a
  // direction that drops focus to <body> without saying so, so they are counted
  // here rather than left to be discovered one at a time.
  const onWalkUp = () => {
    const target = walkUpTarget(parent, walk.current.origin);
    walk.current.claimAt = routeHash(target);
    navigate(target);
  };
  const onSelect = useCallback(
    (entry: NavLevelEntry) => {
      // A row pressed inside the phone sheet closes it: the sheet covers the
      // page it just navigated to. Dismissal on an OUTSIDE click cannot do this,
      // and should not — a preference row inside a popover must be able to act
      // without taking the popover with it.
      onNavigate();
      // A row that OPENS a level is about to be replaced by that level, so it
      // hands its focus on rather than dropping it — to the address it opens,
      // which is the row's own.
      if (entry.children && entry.children.length > 0) {
        walk.current.claimAt = navEntryHref(shown.path, entry);
      }
    },
    [onNavigate, shown, walk],
  );
  // No dependency list, because the condition is the CLAIM rather than a value:
  // the walk is asked for in a handler, and the rows it asks for are only in the
  // document after the address changes — in another rail entirely, when the walk
  // crossed into or out of a section. Every render at any other address finds a
  // claim that is not for it and does nothing.
  useEffect(() => {
    if (walk.current.claimAt !== routeHash(route)) {
      return;
    }
    walk.current.claimAt = undefined;
    container.current
      ?.querySelector<HTMLElement>(".navlevel .navwrap .navitem")
      ?.focus();
  });

  return { depth, shown, parent, onWalkUp, onSelect };
}

type TipState = Readonly<{
  collapsed: boolean;
  tip: string | null;
  onTip: (key: string | null) => void;
}>;

function NavLevelRow({
  level,
  entry,
  count,
  state,
  onSelect,
}: Readonly<{
  level: NavTrailLevel;
  entry: NavLevelEntry;
  count?: number;
  state: TipState;
  onSelect: (entry: NavLevelEntry) => void;
}>) {
  const t = useT();
  // One label, feeding the row text, the aria-label and the collapsed-rail
  // tooltip below — the three read the same string, so a row can never
  // announce one name and show another.
  const label = t(entry.labelKey);
  const active = level.activeId === entry.id;
  const key = navTipKey(level, entry.id);
  return (
    <a
      className={active ? "navitem active" : "navitem"}
      href={navEntryHref(level.path, entry)}
      aria-label={label}
      aria-current={active ? (level.ancestor ? "true" : "page") : undefined}
      onMouseEnter={() => state.onTip(key)}
      onMouseLeave={() => state.onTip(null)}
      onFocus={() => state.onTip(key)}
      onBlur={() => state.onTip(null)}
      onClick={() => onSelect(entry)}
    >
      <entry.icon aria-hidden />
      {/* The label stays mounted and collapses its width, so the transition is
          continuous rather than a pop. aria-label carries the accessible name
          either way. */}
      <span className="navlabel">{label}</span>
      {count !== undefined && count > 0 && (
        <span className="count">{count}</span>
      )}
      {/* Inside the row, not beside it: the tooltip sits outside the row's box
          but within its subtree, so moving the pointer onto it never leaves the
          row and never tears it away mid-read (WCAG 1.4.13, hoverable). A
          sibling could not manage that without making the wrapper itself
          interactive. */}
      {state.collapsed && state.tip === key && (
        <span className="navtip" role="tooltip">
          {label}
        </span>
      )}
    </a>
  );
}

function NavLevelGroupView({
  level,
  group,
  headingTag: Heading,
  counts,
  state,
  onSelect,
}: Readonly<{
  level: NavTrailLevel;
  group: NavLevelGroup;
  headingTag: "h2" | "h3";
  counts?: NavCounts;
  state: TipState;
  onSelect: (entry: NavLevelEntry) => void;
}>) {
  const t = useT();
  return (
    <div className="navgroup">
      {/* The heading keeps its box in both states — collapsed it hides its text
          and draws a hairline inside the same space. Swapping it for a shorter
          <hr> re-spaced every group and drifted the icons. */}
      {group.headingKey && (
        <Heading className="navheading">{t(group.headingKey)}</Heading>
      )}
      {group.items.map((entry) => (
        <div
          className={
            level.barIds?.has(entry.id) ? "navwrap primary" : "navwrap"
          }
          key={entry.id}
        >
          <NavLevelRow
            level={level}
            entry={entry}
            count={
              level.badgeIds?.has(entry.id) ? counts?.[entry.id] : undefined
            }
            state={state}
            onSelect={onSelect}
          />
        </div>
      ))}
    </div>
  );
}

// The way back up. It READS "Back" — the reader knows what they walked down
// from, and the word for it is the same at every depth — while its accessible
// NAME says where it leads: at the section's own level that is the destinations
// it stepped aside for, deeper it is the entry the reader drilled through, and a
// control whose name never changes while its target does is the one a screen
// reader gets wrong. The visible word is contained in that name, which is what
// WCAG 2.5.3 asks of a control labelled shorter than it is named.
function NavLevelBack({
  parent,
  state,
  onWalkUp,
}: Readonly<{
  parent: NavTrailLevel;
  state: TipState;
  onWalkUp: () => void;
}>) {
  const t = useT();
  const name = parent.titleKey ? t(parent.titleKey) : t("shell.navTop");
  const label = t("shell.navBackTo", { name });
  return (
    <button
      type="button"
      className="navitem navback"
      aria-label={label}
      onClick={onWalkUp}
      onMouseEnter={() => state.onTip(BACK_TIP_KEY)}
      onMouseLeave={() => state.onTip(null)}
      onFocus={() => state.onTip(BACK_TIP_KEY)}
      onBlur={() => state.onTip(null)}
    >
      <ChevronLeft aria-hidden />
      <span className="navlabel">{t("shell.navBack")}</span>
      {state.collapsed && state.tip === BACK_TIP_KEY && (
        <span className="navtip" role="tooltip">
          {label}
        </span>
      )}
    </button>
  );
}

// What a level is CALLED: its message key, and nothing when it is the primary
// level (the navigation landmark names that one).
function levelTitle(
  level: NavTrailLevel,
  t: (key: MessageKey) => string,
): string {
  return level.titleKey ? t(level.titleKey) : "";
}

export function NavLevelView({
  level,
  parent,
  counts,
  state,
  onSelect,
  onWalkUp,
}: Readonly<{
  level: NavTrailLevel;
  // Absent on the primary level, which is where the sidebar already is: there
  // is nothing above it to walk back to.
  parent?: NavTrailLevel;
  counts?: NavCounts;
  state: TipState;
  onSelect: (entry: NavLevelEntry) => void;
  onWalkUp: () => void;
}>) {
  const t = useT();
  return (
    <div className={parent ? "navlevel drilled" : "navlevel"}>
      {parent && (
        <NavLevelBack parent={parent} state={state} onWalkUp={onWalkUp} />
      )}
      {levelTitle(level, t) && (
        <h2 className="navtitle">{levelTitle(level, t)}</h2>
      )}
      {level.groups.map((group, index) => (
        <NavLevelGroupView
          key={group.headingKey ?? `group-${index}`}
          level={level}
          group={group}
          // A level that names itself has taken the level-2 heading, so its
          // groups sit under it rather than beside it in the outline.
          headingTag={levelTitle(level, t) ? "h3" : "h2"}
          counts={counts}
          state={state}
          onSelect={onSelect}
        />
      ))}
    </div>
  );
}
