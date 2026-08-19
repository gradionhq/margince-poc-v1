import { useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  Menu,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Settings,
  X,
} from "lucide-react";
import {
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import type { components } from "../api/schema";
import { Button, Modal } from "../design-system/atoms";
import { Logomark } from "../design-system/logomark";
import { useTruncationTooltip } from "../design-system/tooltip";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useEntityName } from "../screens/entityref";
import { SETTINGS_SCREEN, useSettingsSection } from "../screens/settings";
import { AccountMenu, AccountRows } from "./account";
import { AgentDock } from "./agentdock";
import { EconomyBanner } from "./economybanner";
import { EmbedReindexBanner } from "./embedreindexbanner";
import { type EntityKind, SCREEN_ENTITY } from "./entity";
import { EXTENSION_SCREEN, findExtension } from "./extensions";
import {
  GRIDDED_RECORD_SCREENS,
  MOBILE_PRIMARY,
  NAV,
  type NavCounts,
  type NavLevelEntry,
  type NavLevelGroup,
  type NavSection,
  navLevelHref,
  RAIL_LESS_SCREENS,
} from "./nav";
import {
  NavLevelView,
  NavWalkProvider,
  useNavLevel,
  useNavWalk,
  useNavWalkClaim,
} from "./navlevel";
import { paletteHotkeyLabel } from "./palette";
import { usePopoverDismiss } from "./popover";
import { type Route, routeHash, useRoute } from "./router";
import { SorModeChip } from "./sormodechip";
import { uiPreviewTaskbarEnabled } from "./ui-preview";
import { usePhoneViewport } from "./viewport";
import "./shell.css";

type CompanyProfile = components["schemas"]["CompanyProfile"];

// The app shell: one sidebar against the left edge of the viewport, and the
// content beside it. Collapsed the sidebar is the canonical 64px rail WDS-NAV-1
// specifies, unchanged — the labeled state is additive, so the spec stays true
// at 64px rather than being replaced.
//
// The sidebar carries everything that is true for the whole session: where you
// can go, how you search, and who you are signed in as. The content column
// carries only what is true for THIS screen — its heading, its actions, and the
// agent. There is no top bar: a full-width strip of chrome above every screen
// spent the scarcest space in the layout on things the sidebar already holds.

// The attention counts the whole chrome reads — the rail badges them and the
// agent beside the page title reports the same numbers. They are the levels'
// own currency (app/subnav.ts), named here for the shell because App.tsx hands
// them in at that seam.
export type ShellCounts = NavCounts;

const COLLAPSE_KEY = "margince.sidebarCollapsed";
// Comfortably past --shellAnim (0.36s) in shell.css: the two must not disagree.
const SETTLE_MS = 420;

// Storage is unavailable in some embedded contexts; a missing preference is a
// default, never an error.
function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // A browser refusing storage must not break navigation.
  }
}

function BrandBlock() {
  const t = useT();
  // The installation's own organization (A107/ADR-0061: one installation, one
  // organization). Read from the cache the onboarding gate already filled
  // rather than observing ["company"] again: a second observer on that entry
  // re-triggers the gate's fetch and walks the app back through its splash.
  // The gate guarantees the profile is present before the shell mounts.
  // Absent, the block shows the product name only — a company name is never
  // invented to fill the line.
  const installation =
    useQueryClient().getQueryData<CompanyProfile | null>(["company"])
      ?.display_name || undefined;
  return (
    <a className="ws" href="#/home" aria-label={t("shell.logoAria")}>
      <span className="ws-chip">
        <Logomark />
      </span>
      <span className="ws-name">
        <b>{t("shell.logoAria")}</b>
        {installation && <span className="ws-org">{installation}</span>}
      </span>
    </a>
  );
}

// The sidebar's own shortcut, spelled the way the platform spells it — the
// palette's label helper answers the same question for ⌘K.
function collapseHotkeyLabel(platform: string): string {
  return /mac|iphone|ipad|ipod/i.test(platform) ? "⌘B" : "Ctrl B";
}
// The search row's tooltip shares the collapsed rail's tooltip state with the
// destinations, and that state is keyed by screen id. Search is not a screen —
// it opens the palette over the one you are on — so it needs a key of its own
// that no NAV entry can ever take.
const SEARCH_TIP_KEY = "rail-search";

// Search sits directly under the brand, above the destinations: it is how you
// reach anything the ten rows do not list, so it reads as the first way to
// move rather than as an eleventh place to go. It is a BUTTON styled as a row
// and never accepts inline typing (AC-shell-7, one search affordance) — the
// keyboard shortcut beside it is the same palette this opens.
//
// It stays on screen inside a drilled-in level, at every width: ⌘K is invisible
// to anyone who does not already know it, and on touch there is no ⌘K at all, so
// hiding the row leaves a level with no way to search from at all.
function RailSearch({
  collapsed,
  tipOpen,
  onOpenSearch,
  onTip,
}: Readonly<{
  collapsed: boolean;
  tipOpen: boolean;
  onOpenSearch: () => void;
  onTip: (key: string | null) => void;
}>) {
  const t = useT();
  const label = t("shell.search");
  return (
    <div className="railsearchwrap">
      <button
        type="button"
        className="navitem railsearch"
        aria-label={label}
        onClick={onOpenSearch}
        onMouseEnter={() => onTip(SEARCH_TIP_KEY)}
        onMouseLeave={() => onTip(null)}
        onFocus={() => onTip(SEARCH_TIP_KEY)}
        onBlur={() => onTip(null)}
      >
        <Search aria-hidden />
        <span className="navlabel">{label}</span>
        {/* The shortcut is a hint, not a second name: aria-label above already
            says "Search", so a screen reader is not made to spell out ⌘K. */}
        <kbd className="t-mono" aria-hidden>
          {paletteHotkeyLabel(navigator.platform)}
        </kbd>
        {collapsed && tipOpen && (
          <span className="navtip" role="tooltip">
            {label}
          </span>
        )}
      </button>
    </div>
  );
}

// The SETTINGS door's tooltip shares the collapsed rail's tooltip state with the
// destinations, and that state is keyed by screen id. The door is not one of the
// ten, so — like search — it needs a key of its own that no NAV entry can take.
const SETTINGS_TIP_KEY = "rail-settings";

// WCAG 2.4.1, Bypass Blocks. Every page in the product puts the same block ahead
// of its content — the brand, the search, up to eleven navigation rows, the More
// button, the settings door and the account menu — and without this a keyboard
// reader walked all of it again on every page they opened.
//
// A button, not the conventional `<a href="#content">`, because this app is
// hash-routed: setting the hash to "#content" is a NAVIGATION here, and the
// router would parse it as a screen name and land on the unknown-page fallback.
// Fragment links and hash routing cannot both own the hash, so the affordance
// keeps the behaviour (move focus to the content) and gives up the spelling.
//
// It sits first in the DOM so it is the first thing Tab reaches, and stays out of
// sight until it has focus (shell.css).
function SkipToContent({
  target,
}: Readonly<{ target: RefObject<HTMLElement | null> }>) {
  const t = useT();
  return (
    <button
      type="button"
      className="skiplink"
      onClick={() => target.current?.focus()}
    >
      {t("shell.skipToContent")}
    </button>
  );
}

// Settings, at the foot directly above the account block. It is a DOOR rather
// than an eleventh destination: the ten rows above are where the work happens,
// and Settings is a section you go into and come back out of. Without it the
// settings level could only be entered through the account menu's popover — you
// could walk out of the level and never back into it.
//
// It claims the current page ONLY when the level on screen cannot, which is
// what `current` carries. Inside the section the level itself is on screen with
// the reader's own entry current and this stays quiet; on a route that marks
// Settings current without rendering a row for it — a composed unit's screen,
// which lives under Settings without being one of its tabs — this is the only
// element that can say so. The document still claims exactly ONE current page,
// which is the invariant, not the silence.
function RailSettingsDoor({
  collapsed,
  current,
  tipOpen,
  onTip,
}: Readonly<{
  collapsed: boolean;
  current: boolean;
  tipOpen: boolean;
  onTip: (key: string | null) => void;
}>) {
  const t = useT();
  const label = t("nav.settings");
  const claimFocus = useNavWalkClaim();
  const href = routeHash({ screen: SETTINGS_SCREEN });
  return (
    <a
      className={
        current ? "navitem railsettings active" : "navitem railsettings"
      }
      aria-current={current ? "page" : undefined}
      href={href}
      aria-label={label}
      // This link is the walk INTO the section, so it hands its focus on to the
      // level it opens (navlevel.tsx). Without the claim the rail is replaced
      // under a reader standing on this row and focus falls to <body>: the walk
      // out was pinned and the walk in was not, so a keyboard reader could enter
      // Settings and land at the top of the document.
      //
      // Not preventDefault'd, and no navigate() call: the href is what moves,
      // which keeps middle-click and "open in new tab" working. A claim armed by
      // a navigation that never happens is spent by nobody and harmless.
      onClick={() => claimFocus(href)}
      onMouseEnter={() => onTip(SETTINGS_TIP_KEY)}
      onMouseLeave={() => onTip(null)}
      onFocus={() => onTip(SETTINGS_TIP_KEY)}
      onBlur={() => onTip(null)}
    >
      <Settings aria-hidden />
      <span className="navlabel">{label}</span>
      {collapsed && tipOpen && (
        <span className="navtip" role="tooltip">
          {label}
        </span>
      )}
    </a>
  );
}

// The panel's own state as classes: the width it is at, whether the phone sheet
// is open, whether it is mid-collapse, and whether it is showing a drilled-in
// LEVEL. That last one is a class rather than a CSS `:has()` on the level's own
// markup, whose specificity is its argument's and would have outranked the
// sheet's layout from a rule that only means to arrange a panel.
function railClasses(
  state: Readonly<{
    collapsed: boolean;
    sheetOpen: boolean;
    settling: boolean;
    leveled: boolean;
  }>,
): string {
  return [
    "rail",
    state.collapsed ? "collapsed" : "expanded",
    state.sheetOpen ? "sheetopen" : "",
    state.settling ? "settling" : "",
    state.leveled ? "leveled" : "",
  ]
    .filter((name) => name !== "")
    .join(" ");
}

type RailProps = {
  route: Route;
  counts?: ShellCounts;
  collapsed?: boolean;
  onToggle?: () => void;
  onOpenSearch: () => void;
};

export function WorkspaceRail({
  route,
  counts,
  collapsed = false,
  onToggle,
  onOpenSearch,
  section,
}: Readonly<RailProps & { section?: NavSection }>) {
  const t = useT();
  // Collapsed items are icon-only, so the label needs a tooltip that satisfies
  // WCAG 1.4.13: it appears on keyboard focus as well as hover, stays visible
  // while the pointer is on it (it renders inside the hovered wrapper), and is
  // dismissible with Escape without moving focus. The tooltip is never the
  // accessible name — aria-label carries that in both states.
  const [tip, setTip] = useState<string | null>(null);
  // At phone width the same markup is a bottom bar; More expands it into a
  // sheet carrying every destination. One nav element, so there is still exactly
  // one navigation landmark and no second item list to keep in sync.
  const [sheetOpen, setSheetOpen] = useState(false);
  const phone = usePhoneViewport();
  const nav = useRef<HTMLElement>(null);
  const more = useRef<HTMLButtonElement>(null);
  // While the sidebar is mid-collapse the pointer is still inside the head — it
  // has not narrowed past it yet — so the hover rules would hold the toggle on
  // screen at its new centred position and read as the icon sliding into the
  // logomark. Reveal is suppressed until the width settles. Kept slightly longer
  // than --shellAnim so it cannot end mid-transition.
  const [settling, setSettling] = useState(false);
  const settleTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(settleTimer.current), []);
  const handleToggle = useCallback(() => {
    setSettling(true);
    window.clearTimeout(settleTimer.current);
    settleTimer.current = window.setTimeout(
      () => setSettling(false),
      SETTLE_MS,
    );
    onToggle?.();
  }, [onToggle]);
  const dismissTip = useCallback((event: KeyboardEvent) => {
    if (event.key === "Escape") {
      setTip(null);
    }
  }, []);

  // The sheet is a popover over the page, so it dismisses like every other one
  // in the chrome: Escape from anywhere, any click outside it — which is what
  // makes the scrim behind it work without being a control of its own.
  const closeSheet = useCallback(() => setSheetOpen(false), []);
  usePopoverDismiss(sheetOpen, nav, closeSheet);

  // Which of the route's levels the panel is showing, and the two ways the
  // reader moves between them (app/navlevel.tsx). A section's entries take the
  // panel OVER rather than hanging off the destinations: 64px cannot carry two
  // levels, and 252px carrying both reads as a list of twenty places to go.
  //
  // At phone width the panel is a bottom bar of four destinations, and it KEEPS
  // them on a section route — a bar that hands its four tabs over to a section
  // loses every destination and is left holding two controls. The section is
  // reached from the page head there instead (SectionSwitcher below), so no
  // section is walked into here.
  const level = useNavLevel(
    route,
    phone ? undefined : section,
    nav,
    closeSheet,
  );

  // Opening a sheet that covers the page has to take focus with it, or a
  // keyboard user is left tabbing through a page they can no longer see; and
  // closing it has to hand focus back to the control that opened it rather than
  // dropping it on <body>. Only when the sheet still HOLDS focus — a row that
  // navigated away has already put focus somewhere better — and only once the
  // sheet has actually been open: this effect also runs on MOUNT, where a rail
  // arriving with the level's first row already focused (a walk that crossed into
  // or out of a section mounts a new rail) would have that focus taken straight
  // off it and put on More.
  const sheetOpened = useRef(false);
  useEffect(() => {
    if (sheetOpen) {
      sheetOpened.current = true;
      nav.current?.querySelector<HTMLElement>(".navwrap .navitem")?.focus();
      return;
    }
    if (sheetOpened.current && nav.current?.contains(document.activeElement)) {
      sheetOpened.current = false;
      more.current?.focus();
    }
  }, [sheetOpen]);

  // The sheet exists only at phone width — the control that closes it is not
  // rendered above the breakpoint. Widening the window while it is open would
  // otherwise leave the page locked and inert with nothing on screen to release
  // it. The width is already subscribed to above, so this reads that answer
  // rather than opening a second media query of its own.
  useEffect(() => {
    if (sheetOpen && !phone) {
      setSheetOpen(false);
    }
  }, [sheetOpen, phone]);

  // A scrim that only LOOKS blocking is the worst of both worlds: the page it
  // dims stays reachable by Tab and by a screen reader. `inert` on the content
  // column takes it out of the tab order, out of the accessibility tree and out
  // of pointer reach in one attribute — the same guarantee <dialog> gives, which
  // this nav cannot become without giving up its landmark. The body stops
  // scrolling for the same reason: a touch that starts on the scrim should
  // dismiss, not scroll the page underneath.
  useEffect(() => {
    if (!sheetOpen) {
      return;
    }
    const main = document.querySelector<HTMLElement>(".main");
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    if (main) {
      main.inert = true;
    }
    return () => {
      document.body.style.overflow = previous;
      if (main) {
        main.inert = false;
      }
    };
  }, [sheetOpen]);

  // A nav destination that the phone bar hides behind More: on those routes More
  // is the current tab, since the row that would carry the state is not rendered.
  const inSheet = NAV.some(
    (item) => item.screen === route.screen && !MOBILE_PRIMARY.has(item.screen),
  );

  // Whether the Settings door is the thing that says where you are.
  //
  // A route can mark `settings` current while no ROW on screen carries that id:
  // a composed unit's screen does exactly that (nav.ts's activeRowFor), because
  // a unit lives under Settings without being one of its tabs. The door is then
  // the only element that can claim the page, and without this the rail marks
  // NOTHING current on every unit route.
  //
  // Derived from the level rather than from a list of routes, so it stays true
  // for whatever else comes to mark Settings current — and so it cannot produce
  // a second current element: the moment a rendered row carries the id, this is
  // false and the row answers instead.
  const settingsIsCurrent =
    level.shown.activeId === SETTINGS_SCREEN &&
    !level.shown.groups.some((group) =>
      group.items.some((item) => item.id === SETTINGS_SCREEN),
    );

  return (
    <>
      {/* The scrim dims the page the sheet covers and gives the eye the layer
          boundary. It carries no behaviour of its own: a click on it lands
          outside the nav, which is already what closes the sheet. */}
      {sheetOpen && <div className="railscrim" aria-hidden="true" />}
      <nav
        ref={nav}
        className={railClasses({
          collapsed,
          sheetOpen,
          settling,
          leveled: level.parent !== undefined,
        })}
        aria-label={t("shell.railAria")}
        onKeyDown={dismissTip}
      >
        <div className="railhead">
          <BrandBlock />
          {onToggle && (
            <button
              type="button"
              className="railtoggle"
              aria-label={collapsed ? t("shell.expand") : t("shell.collapse")}
              // The shortcut belongs in the tooltip, not in the accessible
              // name: a speech-input user says the words they can read, and
              // "Collapse sidebar ⌘B" is not one of them.
              title={`${
                collapsed ? t("shell.expand") : t("shell.collapse")
              } · ${collapseHotkeyLabel(navigator.platform)}`}
              aria-expanded={!collapsed}
              onClick={handleToggle}
            >
              {collapsed ? (
                <PanelLeftOpen size={17} aria-hidden />
              ) : (
                <PanelLeftClose size={17} aria-hidden />
              )}
            </button>
          )}
        </div>
        <RailSearch
          collapsed={collapsed}
          tipOpen={tip === SEARCH_TIP_KEY}
          onOpenSearch={onOpenSearch}
          onTip={setTip}
        />
        {/* Keyed by depth so a level that arrives is a new element and plays its
            entrance; two addresses at the SAME depth are the same level with
            another row current, and nothing should move. */}
        <NavLevelView
          key={level.depth}
          level={level.shown}
          parent={level.parent}
          counts={counts}
          state={{ collapsed, tip, onTip: setTip }}
          onSelect={level.onSelect}
          onWalkUp={level.onWalkUp}
        />
        {/* Phone-width only: expands the bar into a sheet carrying every
          destination. Hidden by CSS on the desktop sidebar.
          It carries the active state for every destination it hides, so the
          closed bar always shows where you are — the four tabs cannot, because
          those routes' own rows are display:none at this width. */}
        <button
          type="button"
          ref={more}
          className={inSheet ? "railmore active" : "railmore"}
          // Open, this control closes the sheet — so it says so, in the name as
          // well as in the glyph. A control whose name stays the same while its
          // job changes is the one a screen reader gets wrong.
          aria-label={sheetOpen ? t("shell.closeMenu") : t("shell.more")}
          aria-expanded={sheetOpen}
          // The state has to reach a screen reader, not just the eye: the hidden
          // route's own link is out of the accessibility tree at this width, so
          // without this nothing in the bar reports the current page. Dropped once
          // the sheet is open, because the real row is then visible and carrying
          // it — two elements claiming the current page is worse than none.
          aria-current={inSheet && !sheetOpen ? "page" : undefined}
          onClick={() => setSheetOpen((open) => !open)}
        >
          {sheetOpen ? <X aria-hidden /> : <Menu aria-hidden />}
          <span className="navlabel">
            {sheetOpen ? t("shell.closeMenu") : t("shell.more")}
          </span>
        </button>
        <div className="grow" />
        {/* Who you are signed in as, at the foot where a dashboard sidebar keeps
          it — and the menu opens upward from there, so it never covers the
          destinations you were reading. */}
        <div className="railfoot">
          {/* The way INTO Settings, above the person it belongs to. Hidden at
            phone width (shell.css), where the sheet's account rows already
            offer it and the bar has no room for a sixth control. */}
          <RailSettingsDoor
            collapsed={collapsed}
            current={settingsIsCurrent}
            tipOpen={tip === SETTINGS_TIP_KEY}
            onTip={setTip}
          />
          {/* In the sheet the rows stand on their own. The sheet exists only at
            phone width, where the foot has no room above it for a popover to
            open into — anchored to the bottom of the viewport the menu had
            nowhere to go, so the rows it hides were unreachable. Everywhere else
            the rail carries ONE account affordance, which is the menu. */}
          {sheetOpen ? <AccountRows /> : <AccountMenu collapsed={collapsed} />}
        </div>
      </nav>
    </>
  );
}

/**
 * The rail on a settings route, carrying the settings level.
 *
 * Which settings entries exist for a principal is a GRANT question, and grants
 * belong to the screen whose cards ask for them (screens/settings.tsx) — the
 * shell only ever receives a finished section. Mounted on that route alone, so
 * no other screen pays for the visibility probes the hook makes.
 */
export function SettingsRail(props: Readonly<RailProps>) {
  const section = useSettingsSection(props.route.id);
  return <WorkspaceRail {...props} section={section} />;
}

/**
 * The page head on a settings route, naming the tab the reader opened.
 *
 * It reads the same section the rail does — the hook derives it from the
 * capability cache the rail already warmed, so the two cannot disagree about
 * which tab is current, and neither can the content column, which resolves it
 * from that same hook.
 */
function SettingsPageHead({
  route,
  actions,
  counts,
}: Readonly<{ route: Route; actions?: ReactNode; counts?: ShellCounts }>) {
  const section = useSettingsSection(route.id);
  return (
    <PageHead
      route={route}
      actions={actions}
      counts={counts}
      section={section}
    />
  );
}

// The line under the page's name, for the screens whose name alone does not
// say what the page is for. It belongs to the page rather than to anything on
// it, which is why it lives beside the title keys: a screen that printed its
// own subtitle had to print its own title above it to hang it on, and the
// shell was already printing that title — the page then named itself twice.
//
// Only a subtitle true of the WHOLE page qualifies. Copy that describes the
// current tab, filter or segment belongs beside that control, where it changes
// with it; the page head cannot see those and would go stale.
const PAGE_SUB_KEYS: Record<string, MessageKey> = {
  inbox: "inbox.sub",
  ai: "ai.sub",
  dedupe: "dedupe.intro",
};

// Off-rail screens (reached from Settings, not the NAV rail) carry their own
// title key. Every authenticated route resolves to real copy — a raw screen
// slug is never shown as a page title.
const OFF_RAIL_TITLE_KEYS: Record<string, MessageKey> = {
  settings: "nav.settings",
  offers: "nav.offers",
  partners: "nav.partners",
  share: "nav.share",
  search: "nav.search",
};

// The record's name at the end of the trail: unresolved (loading, or a record
// this principal cannot read) falls back to the id in mono rather than to
// nothing.
function RecordName({ kind, id }: Readonly<{ kind: EntityKind; id: string }>) {
  const name = useEntityName(kind, id);
  // Truncated to keep the trail on one line, so the tooltip is what carries a
  // name too long for it.
  const text = name ?? id;
  const tip = useTruncationTooltip<HTMLSpanElement>(text);
  return (
    <span
      className={name ? "pagecrumb-name" : "pagecrumb-name t-mono"}
      ref={tip.ref}
      {...tip.trigger}
    >
      {text}
      {tip.tip}
    </span>
  );
}

function resolveTitle(
  screen: string,
  labelKey: MessageKey | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (labelKey) {
    return t(labelKey);
  }
  const offRailKey = OFF_RAIL_TITLE_KEYS[screen];
  // An address nobody in this app answers. The screen under it says so in
  // words; the heading must not print the slug the reader typed as though the
  // product had a page by that name.
  return offRailKey ? t(offRailKey) : t("shell.unknownPage");
}

// What a section contributes to the page head: the entry the reader opened. The
// section's own `activeId` is its answer and comes first, fallbacks included; the
// route's segment stands in only for a caller that resolved nothing.
function sectionHead(
  section: NavSection | undefined,
  route: Route,
): { section: NavSection; entry: NavLevelEntry } | undefined {
  if (!section || section.screen !== route.screen) {
    return undefined;
  }
  const activeId = section.activeId ?? route.id;
  for (const group of section.groups) {
    const entry = group.items.find((item) => item.id === activeId);
    if (entry) {
      return { section, entry };
    }
  }
  return undefined;
}

// One group of the switcher's list, with the section's own heading above it — the
// same grouping the sidebar's level shows, because it is the same data.
function SectionPickGroup({
  section,
  group,
  activeId,
  onPick,
}: Readonly<{
  section: NavSection;
  group: NavLevelGroup;
  activeId: string;
  onPick: () => void;
}>) {
  const t = useT();
  return (
    <div className="sectionpickgroup">
      {group.headingKey && <h3>{t(group.headingKey)}</h3>}
      {group.items.map((entry) => (
        <a
          key={entry.id}
          href={navLevelHref([section.screen], entry.id)}
          aria-current={entry.id === activeId ? "page" : undefined}
          // The sheet covers the page it just navigated to, so a row that acts
          // takes the sheet with it.
          onClick={onPick}
        >
          <entry.icon size={16} aria-hidden />
          {t(entry.labelKey)}
        </a>
      ))}
    </div>
  );
}

/**
 * The way between a section's pages at phone width.
 *
 * The sidebar cannot carry them there — it is a bar of four destinations, and
 * handing those four to a section loses the whole product's navigation — so the
 * section lives in the page head instead: a control naming the entry you are on,
 * opening the section's own list.
 *
 * It opens the design system's `Modal`, which IS the full-screen sheet at this
 * width; a second sheet hand-rolled here would be a second set of dismissal,
 * focus and scroll-lock rules to keep in step with it.
 *
 * The LIST claims the current page and the button does not: the entries are the
 * section's navigation, the same links the sidebar's level carries above the
 * breakpoint, while the button is a control that opens them — and a control is
 * not a page. Only one element in the document may make that claim.
 */
function SectionSwitcher({
  section,
  entry,
}: Readonly<{ section: NavSection; entry: NavLevelEntry }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const titleId = useId();
  const close = useCallback(() => setOpen(false), []);
  const label = t(entry.labelKey);
  return (
    <>
      <button
        type="button"
        className="pageswitch"
        aria-haspopup="dialog"
        aria-expanded={open}
        // The visible word is the entry the reader is on; the accessible name
        // adds what the control does and keeps that word inside it, which is
        // what WCAG 2.5.3 asks of a control named longer than it reads.
        aria-label={t("shell.sectionSwitch", { name: label })}
        onClick={() => setOpen(true)}
      >
        <span>{label}</span>
        <ChevronDown size={16} aria-hidden />
      </button>
      <Modal open={open} onClose={close} labelledBy={titleId}>
        {/* Named by the SECTION: the list is everything Settings holds, and the
            entry the reader came from is marked inside it. */}
        <h2 id={titleId} className="t-h2">
          {t(section.titleKey)}
        </h2>
        <div className="sectionpick">
          {section.groups.map((group, index) => (
            <SectionPickGroup
              key={group.headingKey ?? `group-${index}`}
              section={section}
              group={group}
              activeId={entry.id}
              onPick={close}
            />
          ))}
        </div>
        {/* At this width the dialog is a full-screen sheet: there is no backdrop
            left to click and a touch reader has no Escape, so the way out has to
            be a control in the sheet. */}
        <div className="actions">
          <Button small onClick={close}>
            {t("shell.closeMenu")}
          </Button>
        </div>
      </Modal>
    </>
  );
}

// The page head: the heading of the screen you are on, and beside it the two
// things that are true of the whole product rather than of this screen — which
// system of record is answering, and the agent. It is not a bar: no panel, no
// border, no shadow, so the heading sits on the same ground as the content
// under it and the eye reads one column rather than a stack of frames.
export function PageHead({
  route,
  actions,
  counts,
  section,
}: Readonly<{
  route: Route;
  actions?: ReactNode;
  counts?: ShellCounts;
  section?: NavSection;
}>) {
  const t = useT();
  const phone = usePhoneViewport();

  const navItem = NAV.find((item) => item.screen === route.screen);
  const inSection = sectionHead(section, route);
  // On a screen that publishes a level, the page is the ENTRY the reader opened
  // rather than the section they opened it from: the sidebar's level carries the
  // section's name, and printing it here too named the section twice and the
  // surface never — a settings page read "Settings" above a heading reading
  // "Settings" with the audit log under both.
  //
  // At phone width the sidebar is showing the destinations instead, so nothing
  // else on screen says which section this page belongs to: the pair swaps, the
  // heading names the section, and the switcher under it names the entry and
  // opens the others. Each of the two is still named exactly once.
  const entryTitle = inSection ? t(inSection.entry.labelKey) : undefined;
  const sectionTitle =
    phone && inSection ? t(inSection.section.titleKey) : undefined;
  const title =
    sectionTitle ??
    entryTitle ??
    resolveTitle(route.screen, navItem?.labelKey, t);
  // A record kind, and only then: an id segment that names no record is a
  // screen's own state — the settings tab, for one — and the page is still the
  // screen. Printing that slug as the page's name gave Settings an h1 reading
  // "privacy".
  const recordKind = route.id ? SCREEN_ENTITY[route.screen] : undefined;
  // A composed unit names its own page, so the head yields to it exactly as it
  // yields to a record — and for the same reason: the unit's screen prints its
  // name, and a second one at heading level would leave a screen reader picking
  // between two page titles.
  //
  // Conditioned on the DESCRIPTOR resolving, not on the screen slug alone. A
  // unit route is deliberately absent from both the NAV rail and
  // OFF_RAIL_TITLE_KEYS, so resolveTitle fell through to shell.unknownPage and
  // printed "Not found" over every unit surface an installation answers. But a
  // unit this installation did not compose is genuinely an unknown page, and the
  // screen under it says so in words — so that case keeps the heading rather
  // than yielding to a surface that will not name itself either.
  //
  // No crumb, unlike a record: a record's trail leads back to its list, and
  // there is no `#/ext` index to lead back to. A unit has no row in the rail at
  // all — it is offered from Settings, on the page holding the credential it is
  // configured with (screens/extension-units.tsx), and the Settings door is
  // what the rail marks current while one is open. What would go HERE is a
  // level a unit publishes BELOW its own screen, and none does.
  const unitNamesPage =
    route.screen === EXTENSION_SCREEN && findExtension(route.id) !== null;
  // Read only on the branch that prints an h1: a record surface and a composed
  // unit name themselves, and a subtitle under a crumb would describe the list
  // the crumb leads back to rather than the record on screen.
  const subKey = PAGE_SUB_KEYS[route.screen];

  return (
    <>
      <header className="pagehead">
        <div className="pagetitle">
          {/* A list, a report, a settings surface: the shell names it, and that
            name is the page's one h1 — before this the only page-level name was
            a span in the top bar, so the document had no heading to jump to.
            A RECORD names itself: its surface prints the identity block, so the
            head yields and shows the trail that leads here instead. Printing
            the name twice, once at heading level and once beside the avatar,
            would leave a screen reader picking between two page titles. */}
          {unitNamesPage ? null : recordKind && route.id ? (
            <p className="pagecrumb">
              <a
                className="pageback"
                href={routeHash({ screen: route.screen })}
              >
                {navItem && (
                  <navItem.icon
                    size={14}
                    strokeWidth={1.8}
                    aria-hidden="true"
                  />
                )}
                {title}
              </a>
              <span aria-hidden="true">/</span>
              <RecordName kind={recordKind} id={route.id} />
            </p>
          ) : (
            <>
              <h1 className="t-display">{title}</h1>
              {subKey && <p className="pagesub">{t(subKey)}</p>}
            </>
          )}
        </div>
        <div className="pageaside">
          {actions}
          <SorModeChip />
          {/* One agent surface at a time: with the taskbar preview on, the bar at
              the bottom of the viewport IS the agent, and a second chip beside
              the page title reports the same thing in the same words two feet
              away (app/ui-preview.ts). */}
          {uiPreviewTaskbarEnabled() ? null : (
            <AgentDock approvalsWaiting={counts?.inbox} />
          )}
        </div>
      </header>
      {/* Beside the heading it was a control wedged into the page's title; under
          the head it is what it does — the row you change the section from,
          spanning the column the content below it stands in. */}
      {phone && inSection && (
        <div className="pageswitchwrap">
          <SectionSwitcher
            section={inSection.section}
            entry={inSection.entry}
          />
        </div>
      )}
    </>
  );
}

export function Shell({
  children,
  counts,
  onOpenSearch,
  pageActions,
}: Readonly<{
  children: ReactNode;
  counts?: ShellCounts;
  onOpenSearch: () => void;
  pageActions?: ReactNode;
}>) {
  const route = useRoute();
  const railless = RAIL_LESS_SCREENS.has(route.screen);
  const leveled = route.screen === SETTINGS_SCREEN;
  // Record pages only: the id is what makes it one. `#/companies` is the list,
  // and a list belongs to the other family.
  const griddedRecord =
    route.id !== undefined && GRIDDED_RECORD_SCREENS.has(route.screen);
  const gridded = leveled || griddedRecord;
  const [collapsed, setCollapsed] = useState(
    () => readStored(COLLAPSE_KEY) === "1",
  );
  // What the sidebar's walk between levels remembers — where a walk OUT of a
  // level returns to, and whether the level that arrives was asked for and takes
  // focus. The shell holds it because the shell outlives every rail: the rail on
  // a section route is a different component, mounted on the way in and gone
  // again on the way out.
  const walk = useNavWalk(route, !railless && !leveled);
  const contentRegion = useRef<HTMLElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  // A route change opens a different page, and a page opens at its top. Nothing
  // in the browser does this for us here: the document itself never scrolls
  // (`.app` is exactly one viewport tall), the content COLUMN does, and that
  // column is the same element on every route — so it carries the offset the
  // last page was left at straight into the next one. Reading a scrolled AI
  // settings page and then opening another entry landed the reader partway down
  // whatever the new page happened to have at that offset, and fewer, longer
  // settings pages made it worse.
  //
  // Keyed by the ADDRESS, not by `route`: useRoute parses a fresh object every
  // render, so a dependency on the object would re-run this on every keystroke a
  // screen handles and fight a reader who has scrolled deliberately.
  // Assigning scrollTop rather than calling scrollTo: it is the same instant jump,
  // it is what the list surface's own reset already uses, and it is a property
  // every environment the tests run in actually has.
  const address = routeHash(route);
  // biome-ignore lint/correctness/useExhaustiveDependencies(address): the effect never reads it, which is the point. It is the trigger — the address changing IS the event this reacts to, exactly as the list surface's own scroll reset is triggered by the query it never reads.
  useEffect(() => {
    const column = scroller.current;
    if (column) {
      column.scrollTop = 0;
    }
  }, [address]);

  const toggle = useCallback(() => {
    setCollapsed((current) => {
      const next = !current;
      writeStored(COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });
  }, []);

  useEffect(() => {
    document.body.dataset.screen = route.screen;
  }, [route.screen]);

  if (railless) {
    return (
      <div className="app railless">
        {/* No skip link here, and that is not an omission: these routes carry no
            navigation, so there is no repeated block ahead of the content for a
            reader to skip past (WCAG 2.4.1). */}
        <main className="main">
          <div className="scroll" ref={scroller}>
            {children}
          </div>
        </main>
      </div>
    );
  }

  const railProps: RailProps = {
    route,
    counts,
    collapsed,
    onToggle: toggle,
    onOpenSearch,
  };

  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <SkipToContent target={contentRegion} />
      {/* One rail, two suppliers of what it shows: a screen owning a level wires
          its own data in, everything else renders the destinations alone. The
          provider spans both, because the way out of a level is asked for on the
          rail that has one and answered with what the other one saw. */}
      <NavWalkProvider value={walk}>
        {leveled ? (
          <SettingsRail {...railProps} />
        ) : (
          <WorkspaceRail {...railProps} />
        )}
      </NavWalkProvider>
      {/* Focusable only as the skip link's destination — never a tab stop of its
          own, which is what tabIndex -1 buys. A reader who takes the skip lands
          here, and the next Tab continues into the page's own controls. */}
      <main
        className={gridded ? "main main-gridded" : "main"}
        ref={contentRegion}
        tabIndex={-1}
      >
        {leveled ? (
          <SettingsPageHead
            route={route}
            actions={pageActions}
            counts={counts}
          />
        ) : (
          <PageHead route={route} actions={pageActions} counts={counts} />
        )}
        {/* Public, onboarding, and preference routes are intentionally
            railless; these advisories belong only here. */}
        <EconomyBanner />
        <EmbedReindexBanner />
        <div className="scroll" ref={scroller}>
          {children}
        </div>
      </main>
    </div>
  );
}

export { navigate } from "./router";
export { useRoute };
