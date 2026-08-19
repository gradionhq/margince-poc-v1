import { Bell, PanelLeftClose, PanelLeftOpen, Search } from "lucide-react";
import { Breadcrumb, type Crumb } from "../design-system/breadcrumb";
import { useT } from "../i18n";
import { SETTINGS_SCREEN } from "../screens/settings";
import { AccountMenu } from "./account";
import { SCREEN_ENTITY } from "./entity";
import { EXTENSION_SCREEN, findExtension } from "./extensions";
import { NAV, type NavCounts, type NavSection } from "./nav";
import {
  OFF_RAIL_TITLE_KEYS,
  resolveTitle,
  sectionHead,
  useRouteSubject,
} from "./pagemeta";
import { paletteHotkeyCaps } from "./palette";
import { type Route, routeHash } from "./router";
import { SorModeChip } from "./sormodechip";
import "./topbar.css";

// The top bar: the one strip that is true of the whole session rather than of
// the page under it — where you are (the trail), how you reach anything (the
// search), which system of record is answering, and who you are signed in as.
//
// It stands on the sidebar's own ground with a rule under it, so the chrome
// reads as one L-shaped frame around the content rather than as two panels that
// happen to touch. The page's own name is NOT here: it belongs to the document,
// scrolls with it, and stands in the content column above the content it names.
//
// Three tracks, the outer two equal, so the search sits at the middle of the
// CONTENT column — the reference this follows centres there too, and a field
// centred on the window sits visibly left of the column it stands in.

// The sidebar's shortcut, spelled the way the platform spells it — the palette's
// label helper answers the same question for ⌘K.
function collapseHotkeyLabel(platform: string): string {
  return /mac|iphone|ipad|ipod/i.test(platform) ? "⌘B" : "Ctrl B";
}

/**
 * The trail for a route: where this page sits, ending in the page itself.
 *
 * A hook rather than a function because the last segment of a record's trail is
 * the record's NAME, which is a read — and a trail that printed a uuid until the
 * read landed would be a different sentence on every page open. `useEntityName`
 * is called on every route so the hook order never depends on which page is on
 * screen; it makes no request without an id to resolve.
 */
function useCrumbs(route: Route, section?: NavSection): readonly Crumb[] {
  const t = useT();
  const navItem = NAV.find((item) => item.screen === route.screen);
  // A record kind, and only then: an id segment that names no record is a
  // screen's own state — the settings tab, for one — and the page is still the
  // screen.
  const recordKind = route.id ? SCREEN_ENTITY[route.screen] : undefined;
  const subject = useRouteSubject(route);
  const inSection = sectionHead(section, route);
  const unit =
    route.screen === EXTENSION_SCREEN ? findExtension(route.id) : null;

  if (recordKind && route.id) {
    // A record's trail leads back to the list it was opened from, which is the
    // one place the reader can go that is not "somewhere else entirely".
    return [
      {
        label: resolveTitle(route.screen, navItem?.labelKey, t),
        href: routeHash({ screen: route.screen }),
      },
      { label: subject },
    ];
  }
  if (unit) {
    // A composed unit lives under Settings — it is offered from the page
    // holding the credential it is configured with, and has no index of its own.
    // Its leaf is the SUBJECT, like every other leaf here: the name the unit
    // carries is resolved once, in pagemeta, so the trail and the agent below it
    // cannot disagree about what this page is.
    return [
      {
        label: t(OFF_RAIL_TITLE_KEYS.settings),
        href: routeHash({ screen: SETTINGS_SCREEN }),
      },
      { label: subject },
    ];
  }
  if (inSection) {
    return [
      {
        label: t(inSection.section.titleKey),
        href: routeHash({ screen: route.screen }),
      },
      { label: t(inSection.entry.labelKey) },
    ];
  }
  return [{ label: resolveTitle(route.screen, navItem?.labelKey, t) }];
}

/**
 * The search affordance, centred in the bar at every width that can hold it.
 *
 * It is a BUTTON styled as a field and never accepts inline typing (AC-shell-7,
 * one search affordance): what it opens is the command palette, which is also
 * what ⌘K opens. Below the width where a centred field and the trail beside it
 * would collide it drops to its glyph — the shortcut is invisible to anyone who
 * does not already know it, and on touch there is no ⌘K at all, so the
 * affordance itself may never leave.
 */
function TopBarSearch({
  onOpenSearch,
}: Readonly<{ onOpenSearch: () => void }>) {
  const t = useT();
  const label = t("shell.searchEverything");
  return (
    <div className="topbar-searchslot">
      <button
        type="button"
        className="topbar-search"
        aria-label={label}
        onClick={onOpenSearch}
      >
        <Search size={15} strokeWidth={1.8} aria-hidden />
        <span className="topbar-searchlabel">{label}</span>
        {/* One cap per KEY, which is how a keyboard shortcut is read: "⌘ then K",
            not the four-character string "⌘K" in one box. The caps come from the
            palette's own helper, which is also what the label is joined from, so
            the two spellings ("⌘ K" and "Ctrl K") stay one source.
            The whole group is a hint rather than a second name: the aria-label
            above already says what this control does, so nobody is made to
            listen to the shortcut spelled out. */}
        <span className="topbar-keys" aria-hidden>
          {paletteHotkeyCaps(navigator.platform).map((cap) => (
            <kbd key={cap}>{cap}</kbd>
          ))}
        </span>
      </button>
    </div>
  );
}

/**
 * What is waiting on this person, wherever they are.
 *
 * Approvals are the product's one queue that BLOCKS somebody else — nothing an
 * agent staged runs until a human answers it — so the count belongs in the strip
 * that is on screen on every route, not only on the sidebar row a collapsed rail
 * draws as an icon. It is a link rather than a menu: what to do about the count
 * is the Approvals surface, and a popover restating it would be a second inbox.
 *
 * Silent at zero. A bell that is always on screen with nothing behind it teaches
 * a reader to stop looking at it, which is the one thing a queue cannot afford.
 */
function ApprovalsBell({ waiting }: Readonly<{ waiting?: number }>) {
  const t = useT();
  const count = waiting ?? 0;
  return (
    <a
      className="topbar-bell"
      href={routeHash({ screen: "inbox" })}
      aria-label={
        count > 0
          ? t("shell.approvalsWaiting", { count })
          : t("shell.approvals")
      }
    >
      <Bell size={17} strokeWidth={1.8} aria-hidden />
      {/* The number is in the name above, so the chip is decoration: read out it
          would say the count twice, and the second time without the word for
          what is counted. */}
      {count > 0 && (
        <span className="topbar-bellcount" aria-hidden>
          {count}
        </span>
      )}
    </a>
  );
}

export function TopBar({
  route,
  section,
  counts,
  collapsed,
  onToggle,
  onOpenSearch,
}: Readonly<{
  route: Route;
  section?: NavSection;
  counts?: NavCounts;
  collapsed: boolean;
  onToggle?: () => void;
  onOpenSearch: () => void;
}>) {
  const t = useT();
  const crumbs = useCrumbs(route, section);
  const toggleLabel = collapsed ? t("shell.expand") : t("shell.collapse");
  return (
    <header className="topbar">
      <div className="topbar-lead">
        {onToggle && (
          <button
            type="button"
            className="topbar-toggle"
            aria-label={toggleLabel}
            // The shortcut belongs in the tooltip, not in the accessible name:
            // a speech-input user says the words they can read, and "Collapse
            // sidebar ⌘B" is not one of them.
            title={`${toggleLabel} · ${collapseHotkeyLabel(navigator.platform)}`}
            aria-expanded={!collapsed}
            onClick={onToggle}
          >
            {collapsed ? (
              <PanelLeftOpen size={17} aria-hidden />
            ) : (
              <PanelLeftClose size={17} aria-hidden />
            )}
          </button>
        )}
        <Breadcrumb items={crumbs} label={t("shell.breadcrumbAria")} />
      </div>
      <TopBarSearch onOpenSearch={onOpenSearch} />
      <div className="topbar-trail">
        <SorModeChip />
        <ApprovalsBell waiting={counts?.inbox} />
        <AccountMenu />
      </div>
    </header>
  );
}
