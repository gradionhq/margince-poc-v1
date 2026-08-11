import { useQueryClient } from "@tanstack/react-query";
import { Menu, PanelLeftClose, PanelLeftOpen, Search, X } from "lucide-react";
import {
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import type { components } from "../api/schema";
import { Logomark } from "../design-system/logomark";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useEntityName } from "../screens/entityref";
import { AccountMenu } from "./account";
import { AgentDock } from "./agentdock";
import { EconomyBanner } from "./economybanner";
import { EmbedReindexBanner } from "./embedreindexbanner";
import { type EntityKind, SCREEN_ENTITY } from "./entity";
import {
  BADGE_SCREENS,
  MOBILE_PRIMARY,
  NAV,
  NAV_GROUPS,
  RAIL_LESS_SCREENS,
} from "./nav";
import { paletteHotkeyLabel } from "./palette";
import { usePopoverDismiss } from "./popover";
import { type Route, routeHash, useRoute } from "./router";
import { SorModeChip } from "./sormodechip";
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

export type ShellCounts = Partial<Record<string, number>>;

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

export function WorkspaceRail({
  route,
  counts,
  collapsed = false,
  onToggle,
  onOpenSearch,
}: Readonly<{
  route: Route;
  counts?: ShellCounts;
  collapsed?: boolean;
  onToggle?: () => void;
  onOpenSearch: () => void;
}>) {
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

  // Opening a sheet that covers the page has to take focus with it, or a
  // keyboard user is left tabbing through a page they can no longer see; and
  // closing it has to hand focus back to the control that opened it rather than
  // dropping it on <body>. Only when the sheet still HOLDS focus — a row that
  // navigated away has already put focus somewhere better.
  useEffect(() => {
    if (sheetOpen) {
      nav.current?.querySelector<HTMLElement>(".navwrap .navitem")?.focus();
      return;
    }
    if (nav.current?.contains(document.activeElement)) {
      more.current?.focus();
    }
  }, [sheetOpen]);

  // The sheet exists only at phone width — the control that closes it is not
  // rendered above 700px. Widening the window while it is open would otherwise
  // leave the page locked and inert with nothing on screen to release it.
  useEffect(() => {
    if (!sheetOpen) {
      return;
    }
    const phone = window.matchMedia("(max-width: 700px)");
    const onChange = () => {
      if (!phone.matches) {
        setSheetOpen(false);
      }
    };
    phone.addEventListener("change", onChange);
    return () => phone.removeEventListener("change", onChange);
  }, [sheetOpen]);

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

  const classes = ["rail", collapsed ? "collapsed" : "expanded"];
  if (sheetOpen) {
    classes.push("sheetopen");
  }
  if (settling) {
    classes.push("settling");
  }

  return (
    <>
      {/* The scrim dims the page the sheet covers and gives the eye the layer
          boundary. It carries no behaviour of its own: a click on it lands
          outside the nav, which is already what closes the sheet. */}
      {sheetOpen && <div className="railscrim" aria-hidden="true" />}
      <nav
        ref={nav}
        className={classes.join(" ")}
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
        {NAV_GROUPS.map((group, index) => (
          <div className="navgroup" key={group.headingKey ?? `group-${index}`}>
            {/* The heading keeps its box in both states — collapsed it hides its
              text and draws a hairline inside the same space. Swapping it for a
              shorter <hr> re-spaced every group and drifted the icons. */}
            {group.headingKey && (
              <h2 className="navheading">{t(group.headingKey)}</h2>
            )}
            {group.items.map((item) => {
              const count = BADGE_SCREENS.has(item.screen)
                ? counts?.[item.screen]
                : undefined;
              const active = route.screen === item.screen;
              const label = t(item.labelKey);
              const wrapClass = MOBILE_PRIMARY.has(item.screen)
                ? "navwrap primary"
                : "navwrap";
              return (
                <div className={wrapClass} key={item.screen}>
                  <a
                    className={active ? "navitem active" : "navitem"}
                    href={routeHash({ screen: item.screen })}
                    aria-label={label}
                    aria-current={active ? "page" : undefined}
                    onMouseEnter={() => setTip(item.screen)}
                    onMouseLeave={() => setTip(null)}
                    onFocus={() => setTip(item.screen)}
                    onBlur={() => setTip(null)}
                    // A destination pressed inside the phone sheet closes it:
                    // the sheet covers the page it just navigated to. Dismissal
                    // on an OUTSIDE click cannot do this, and should not — a
                    // preference row inside a popover must be able to act
                    // without taking the popover with it.
                    onClick={closeSheet}
                  >
                    <item.icon aria-hidden />
                    {/* The label stays mounted and collapses its width, so the
                      transition is continuous rather than a pop. aria-label
                      carries the accessible name either way. */}
                    <span className="navlabel">{label}</span>
                    {count !== undefined && count > 0 && (
                      <span className="count">{count}</span>
                    )}
                    {/* Inside the row, not beside it: the tooltip sits outside the
                      row's box but within its subtree, so moving the pointer onto
                      it never leaves the row and never tears it away mid-read
                      (WCAG 1.4.13, hoverable). A sibling could not manage that
                      without making the wrapper itself interactive. */}
                    {collapsed && tip === item.screen && (
                      <span className="navtip" role="tooltip">
                        {label}
                      </span>
                    )}
                  </a>
                </div>
              );
            })}
          </div>
        ))}
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
          {/* The sheet is the phone's full-width sidebar, so the block prints who
            is signed in there even when the desktop preference it inherits is
            the 64px rail — collapsed is about the rail's width, and inside the
            sheet there is none. (Outside the sheet the foot is hidden at phone
            width, so this only ever reads as the sidebar's own state.) */}
          <AccountMenu collapsed={collapsed && !sheetOpen} />
        </div>
      </nav>
    </>
  );
}

// Off-rail screens (reached from Settings, not the NAV rail) carry their own
// title key. Every authenticated route resolves to real copy — a raw screen
// slug is never shown as a page title.
const OFF_RAIL_TITLE_KEYS: Record<string, MessageKey> = {
  settings: "nav.settings",
  design: "nav.design",
  dedupe: "nav.dedupe",
  products: "nav.products",
  "offer-templates": "nav.offerTemplates",
  "custom-fields": "nav.customFields",
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
  if (name) {
    return <span>{name}</span>;
  }
  return (
    <span className="t-mono" title={id}>
      {id}
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

// The page head: the heading of the screen you are on, and beside it the two
// things that are true of the whole product rather than of this screen — which
// system of record is answering, and the agent. It is not a bar: no panel, no
// border, no shadow, so the heading sits on the same ground as the content
// under it and the eye reads one column rather than a stack of frames.
export function PageHead({
  route,
  actions,
  counts,
}: Readonly<{
  route: Route;
  actions?: ReactNode;
  counts?: ShellCounts;
}>) {
  const t = useT();

  const navItem = NAV.find((item) => item.screen === route.screen);
  const title = resolveTitle(route.screen, navItem?.labelKey, t);
  // A record kind, and only then: an id segment that names no record is a
  // screen's own state — the settings tab, for one — and the page is still the
  // screen. Printing that slug as the page's name gave Settings an h1 reading
  // "privacy".
  const recordKind = route.id ? SCREEN_ENTITY[route.screen] : undefined;

  return (
    <header className="pagehead">
      <div className="pagetitle">
        {/* A list, a report, a settings surface: the shell names it, and that
            name is the page's one h1 — before this the only page-level name was
            a span in the top bar, so the document had no heading to jump to.
            A RECORD names itself: its surface prints the identity block, so the
            head yields and shows the trail that leads here instead. Printing
            the name twice, once at heading level and once beside the avatar,
            would leave a screen reader picking between two page titles. */}
        {recordKind && route.id ? (
          <p className="pagecrumb">
            <a className="pageback" href={routeHash({ screen: route.screen })}>
              {navItem && (
                <navItem.icon size={14} strokeWidth={1.8} aria-hidden="true" />
              )}
              {title}
            </a>
            <span aria-hidden="true">/</span>
            <RecordName kind={recordKind} id={route.id} />
          </p>
        ) : (
          <h1 className="t-display">{title}</h1>
        )}
      </div>
      <div className="pageaside">
        {actions}
        <SorModeChip />
        <AgentDock approvalsWaiting={counts?.inbox} />
      </div>
    </header>
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
  const [collapsed, setCollapsed] = useState(
    () => readStored(COLLAPSE_KEY) === "1",
  );

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
        <main className="main">
          <div className="scroll">{children}</div>
        </main>
      </div>
    );
  }

  return (
    <div className={collapsed ? "app" : "app railexpanded"}>
      <WorkspaceRail
        route={route}
        counts={counts}
        collapsed={collapsed}
        onToggle={toggle}
        onOpenSearch={onOpenSearch}
      />
      <main className="main">
        <PageHead route={route} actions={pageActions} counts={counts} />
        {/* Public, onboarding, and preference routes are intentionally
            railless; these advisories belong only here. */}
        <EconomyBanner />
        <EmbedReindexBanner />
        <div className="scroll">{children}</div>
      </main>
    </div>
  );
}

export { navigate } from "./router";
export { useRoute };
