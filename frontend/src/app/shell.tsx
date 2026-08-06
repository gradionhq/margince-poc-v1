import { useQueryClient } from "@tanstack/react-query";
import {
  LogOut,
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
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import type { components } from "../api/schema";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useLogout, useMe } from "../screens/common";
import { useEntityName } from "../screens/entityref";
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
import { type Route, routeHash, useRoute } from "./router";
import { SorModeChip } from "./sormodechip";
import { ThemeToggle } from "./theme-toggle";
import "./shell.css";

type CompanyProfile = components["schemas"]["CompanyProfile"];

// The app shell: a labeled sidebar that collapses to the canonical 64px rail.
// Collapsed is the rail WDS-NAV-1 specifies, unchanged — the expanded state is
// additive, so the spec stays true at 64px rather than being replaced. The top
// bar shows only what is true for the current state (§2b, the cold-start rule).

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

function Logomark() {
  // The delivered Margin-rule "M" (brand kit geometry, same as the mockups).
  return (
    <svg
      viewBox="0 0 299 230"
      width="19"
      height="14.6"
      fill="none"
      aria-hidden
      role="presentation"
    >
      <path
        d="M141.688 223.911V212.017C141.688 210.362 142.722 209.259 143.239 208.914L160.821 191.849C166.613 186.47 172.198 193.4 172.198 197.02V223.911C172.198 228.048 168.061 229.427 165.993 229.599H147.376C143.239 229.599 141.86 225.807 141.688 223.911Z"
        fill="currentColor"
        fillOpacity=".55"
      />
      <path
        d="M191.312 223.907V164.954C191.312 163.299 192.347 162.196 192.864 161.852L210.446 144.786C216.238 139.408 221.823 146.338 221.823 149.957V223.907C221.823 228.044 217.686 229.423 215.618 229.595H197.001C192.864 229.595 191.485 225.803 191.312 223.907Z"
        fill="currentColor"
        fillOpacity=".8"
      />
      <path
        d="M241 223.886V112.704C241 111.049 242.034 109.946 242.551 109.602L260.134 92.5361C265.926 87.1579 271.511 94.0875 271.511 97.7074V223.886C271.511 228.023 267.374 229.402 265.305 229.574H246.688C242.551 229.574 241.172 225.782 241 223.886Z"
        fill="currentColor"
        fillOpacity=".55"
      />
      <path
        d="M0 29.4771V213.06C0 232.09 40.8535 237.882 40.8535 212.025V94.636C40.8535 90.9127 44.9906 91.5196 46.0249 92.5675C72.2263 119.114 125.974 173.344 131.352 177.895C136.73 182.445 142.556 179.791 144.797 177.895C187.202 135.49 272.219 50.3694 273.046 49.1283C273.874 47.8872 275.115 48.6112 275.632 49.1283C278.735 52.4035 285.147 59.0573 285.975 59.471C293.732 65.1595 298.386 59.6434 298.386 55.851V9.82615C298.386 0 286.492 0 280.803 0H235.296C228.573 0 228.573 8.27414 230.124 9.82554C233.917 13.9626 241.812 22.4436 243.053 23.271C244.294 24.0984 244.259 24.9948 244.087 25.3395C210.301 58.2637 144.797 116.356 142.729 118.424C140.66 120.493 138.419 119.286 137.557 118.424L31.028 16.0316C15.7209 0.724496 0 20.1688 0 29.4771Z"
        fill="currentColor"
      />
    </svg>
  );
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

// The agent panel: the prototype's white card at the sidebar foot — orb, who,
// then two divided rows.
//
// What it may claim is constrained: the runtime knows routing is *configured*,
// it does not continuously prove a provider is reachable, so absent a real
// running job the panel states configuration and never liveness. The activity
// line is example data until a list operation exists behind it (the AI activity
// list has no handler), and it says so on screen rather than passing as real.
//
// The orb is pure CSS, deliberately NOT the Core primitive: the Core paints its
// interior on a canvas, and this sits in permanent chrome on every screen, so it
// would run a render loop for the whole session. The glass shell here uses the
// same technique the Core's own shell does — color-mix over tokens, no literal
// colours — and the interior is layered radial gradients instead of a shader.
function AgentOrb() {
  return <span className="agentorb" aria-hidden />;
}

function AgentPanel({ collapsed }: Readonly<{ collapsed: boolean }>) {
  const t = useT();
  if (collapsed) {
    return (
      <div className="agentfield collapsed">
        <span className="agentcard" title={t("agent.title")}>
          <AgentOrb />
        </span>
      </div>
    );
  }
  return (
    <div className="agentfield">
      <div className="agentcard">
        <div className="agenthead">
          <AgentOrb />
          <span className="agentwho">
            <b>{t("agent.title")}</b>
            <span className="agentstate">{t("agent.configured")}</span>
          </span>
        </div>
        {/* Everything below the rule is example data: the AI activity list has
            no handler, and routing and spend are not wired into the shell yet.
            One marker covers the block rather than each line pretending on its
            own. */}
        <p className="agentactivity">{t("agent.exampleActivity")}</p>
        <div className="agentfoot">
          <span className="agentrouting">{t("agent.exampleRouting")}</span>
          <b>{t("agent.exampleCost")}</b>
        </div>
        <p className="agentfixture">
          <span className="t-mono">{t("agent.fixture")}</span>
        </p>
      </div>
    </div>
  );
}

export function WorkspaceRail({
  route,
  counts,
  collapsed = false,
  onToggle,
}: Readonly<{
  route: Route;
  counts?: ShellCounts;
  collapsed?: boolean;
  onToggle?: () => void;
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
  const dismiss = useCallback((event: KeyboardEvent) => {
    if (event.key === "Escape") {
      setTip(null);
      setSheetOpen(false);
    }
  }, []);

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
    <nav
      className={classes.join(" ")}
      aria-label={t("shell.railAria")}
      onKeyDown={dismiss}
    >
      <div className="railhead">
        <BrandBlock />
        {onToggle && (
          <button
            type="button"
            className="railtoggle"
            aria-label={collapsed ? t("shell.expand") : t("shell.collapse")}
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
        className={inSheet ? "railmore active" : "railmore"}
        aria-label={t("shell.more")}
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
        <span className="navlabel">{t("shell.more")}</span>
      </button>
      <div className="grow" />
      <AgentPanel collapsed={collapsed} />
    </nav>
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

// The record's name as plain text: unresolved (loading, or a record this
// principal cannot read) falls back to the id in mono rather than to nothing.
function CrumbName({ kind, id }: Readonly<{ kind: EntityKind; id: string }>) {
  const name = useEntityName(kind, id);
  return name ? (
    <b>{name}</b>
  ) : (
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
  return offRailKey ? t(offRailKey) : screen;
}

export function TopBar({
  route,
  onOpenSearch,
  actions,
}: Readonly<{
  route: Route;
  onOpenSearch: () => void;
  actions?: ReactNode;
}>) {
  const t = useT();
  const { locale, setLocale } = useLocale();
  const logout = useLogout();

  const navItem = NAV.find((item) => item.screen === route.screen);
  const title = resolveTitle(route.screen, navItem?.labelKey, t);
  const crumbKind = SCREEN_ENTITY[route.screen];

  return (
    <header className="topbar">
      {/* On a record, the SECTION is the link — it goes back to the list — and
          the record's own name is plain text, because you are already on it.
          The name resolves through EntityRef's cache and falls back to the raw
          id in mono when it cannot, rather than showing a blank crumb. */}
      <span className="crumb">
        {route.id ? (
          <a href={routeHash({ screen: route.screen })}>{title}</a>
        ) : (
          <b>{title}</b>
        )}
        {route.id &&
          (crumbKind ? (
            <span>
              {" · "}
              <CrumbName kind={crumbKind} id={route.id} />
            </span>
          ) : (
            <span> · {route.id}</span>
          ))}
      </span>
      <div className="r">
        {actions}
        <SorModeChip />
        {/* AC-shell-7: one search affordance. This is a button styled as a
            field — it opens the palette and never accepts inline typing. */}
        <button
          type="button"
          className="searchbar"
          aria-label={t("shell.search")}
          onClick={onOpenSearch}
        >
          <Search aria-hidden />
          <span className="searchhint">{t("shell.searchHint")}</span>
          <kbd className="t-mono">{paletteHotkeyLabel(navigator.platform)}</kbd>
        </button>
        <button
          type="button"
          className="iconbtn"
          aria-label={
            locale === "de" ? t("locale.toEnglish") : t("locale.toGerman")
          }
          onClick={() => setLocale(locale === "de" ? "en" : "de")}
        >
          <span className="t-mono">{locale === "de" ? "EN" : "DE"}</span>
        </button>
        {/* The top bar's own 32px chrome sizing wins over the control's
            portable default — `.topbar .iconbtn` outranks `.theme-toggle`. */}
        <ThemeToggle className="iconbtn" />
        <AccountMenu logout={logout} />
      </div>
    </header>
  );
}

// The avatar owns Settings and sign-out. A menu rather than
// two chrome buttons: the prototype's top bar carries one account affordance,
// and sign-out beside every screen invites a misclick.
function AccountMenu({
  logout,
}: Readonly<{ logout: ReturnType<typeof useLogout> }>) {
  const t = useT();
  const me = useMe();
  const [open, setOpen] = useState(false);

  const identity = me.data?.user;
  const label = identity?.display_name || identity?.email || "";
  // Initials, or a single letter from the address — never a fabricated name.
  const initials =
    label
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part[0]?.toUpperCase() ?? "")
      .join("") || undefined;

  // Dismissal lives on the document so Escape works from anywhere in the menu
  // and any outside click closes it — the opening click is deferred past so it
  // does not close what it just opened.
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    const onClick = () => setOpen(false);
    document.addEventListener("keydown", onKey);
    const timer = window.setTimeout(
      () => document.addEventListener("click", onClick),
      0,
    );
    return () => {
      document.removeEventListener("keydown", onKey);
      window.clearTimeout(timer);
      document.removeEventListener("click", onClick);
    };
  }, [open]);

  return (
    <div className="account">
      <button
        type="button"
        className="user"
        aria-label={t("shell.accountAria")}
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        {initials ?? <Settings size={15} aria-hidden />}
      </button>
      {open && (
        <div className="accountmenu">
          <a href="#/settings">
            <Settings size={15} aria-hidden />
            {t("nav.settings")}
          </a>
          <button
            type="button"
            disabled={logout.isPending}
            onClick={() => logout.mutate()}
          >
            <LogOut size={15} aria-hidden />
            {t("shell.signOutAria")}
          </button>
        </div>
      )}
    </div>
  );
}

export function Shell({
  children,
  counts,
  onOpenSearch,
  topBarActions,
}: Readonly<{
  children: ReactNode;
  counts?: ShellCounts;
  onOpenSearch: () => void;
  topBarActions?: ReactNode;
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
      />
      <main className="main">
        <TopBar
          route={route}
          onOpenSearch={onOpenSearch}
          actions={topBarActions}
        />
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
