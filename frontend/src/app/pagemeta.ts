import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { useEntityName } from "../screens/entityref";
import { SCREEN_ENTITY } from "./entity";
import { EXTENSION_SCREEN, findExtension } from "./extensions";
import { NAV, type NavLevelEntry, type NavSection } from "./nav";
import type { Route } from "./router";

// What the chrome knows about a page before the page renders: its name, its
// one-line description, and which entry of a section it is. The top bar's trail
// and the content column's heading both answer from here, so the crumb and the
// h1 under it can never name the same page two different things.

// The line under the page's name, for the screens whose name alone does not
// say what the page is for. It belongs to the page rather than to anything on
// it, which is why it lives beside the title keys: a screen that printed its
// own subtitle had to print its own title above it to hang it on, and the
// shell was already printing that title — the page then named itself twice.
//
// Only a subtitle true of the WHOLE page qualifies. Copy that describes the
// current tab, filter or segment belongs beside that control, where it changes
// with it; the page heading cannot see those and would go stale.
export const PAGE_SUB_KEYS: Record<string, MessageKey> = {
  inbox: "inbox.sub",
  ai: "ai.sub",
  dedupe: "dedupe.intro",
  // What the whole surface is for, not what the current object tab holds: the
  // sentence is true of a contact filter and a deal filter alike, which is the
  // test a page-level subtitle has to pass.
  filters: "filters.subtitle",
  // Whose messages these are is the fact the page most needs to state, and it
  // is true of the whole page: a scheduled send is readable only by the person
  // who scheduled it, so nothing here is a queue somebody else can work.
  scheduled: "sched.sub",
};

// Off-rail screens (reached from Settings or from the surface that created the
// thing, not the NAV rail) carry their own title key. Every authenticated route
// resolves to real copy — a raw screen slug is never shown as a page title.
export const OFF_RAIL_TITLE_KEYS: Record<string, MessageKey> = {
  settings: "nav.settings",
  offers: "nav.offers",
  partners: "nav.partners",
  share: "nav.share",
  search: "nav.search",
  // Off the rail deliberately. The rail carries the product's twelve
  // destinations and a queue of one person's own unsent mail is not a twelfth;
  // it is reached from the composer that put a message in it and from the Tasks page,
  // which is where the same rep's other waiting work already lives.
  scheduled: "nav.scheduled",
};

export function resolveTitle(
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

// What a section contributes to the chrome: the entry the reader opened. The
// section's own `activeId` is its answer and comes first, fallbacks included; the
// route's segment stands in only for a caller that resolved nothing.
export function sectionHead(
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

/**
 * What this page is ABOUT, in the words a reader would use: the record's name on
 * a record route, the page's own name everywhere else.
 *
 * One spelling, because two surfaces ask the same question and a reader sees
 * both at once — the trail at the top of the window and the agent at the foot of
 * the column. The agent asked it separately once and printed the route's raw id,
 * so the chrome offered to answer questions about `01a01811-c847-…` while the
 * trail two inches above it said "Carol Wagner".
 *
 * A hook, because the answer is a read. `useEntityName` is called on every route
 * so the hook order never depends on which page is on screen, and it makes no
 * request without an id to resolve.
 *
 * The three ways a read can leave this without a name are not one case. A read
 * that FAILED says so: the id is the thing the reader cannot act on, and
 * printing it for a 403 made a refusal indistinguishable from a record whose
 * display field is blank. A read still in flight, and one that answered with no
 * name, both fall back to the id — which is not a name, but is true, and is what
 * the reader can quote.
 */
export function useRouteSubject(route: Route): string {
  const t = useT();
  const navItem = NAV.find((item) => item.screen === route.screen);
  // A composed unit's page is named by the UNIT, which is not in `NAV` and has
  // no title key: without this the subject fell through to `shell.unknownPage`
  // while the trail two inches above it printed the unit's name, and the agent
  // offered to answer questions about "Unknown page" on a page the product
  // knows perfectly well.
  const unit =
    route.screen === EXTENSION_SCREEN ? findExtension(route.id) : null;
  // A record kind, and only then: an id segment that names no record is a
  // screen's own state — the settings tab, for one — and the subject is still
  // the page.
  const recordKind = route.id ? SCREEN_ENTITY[route.screen] : undefined;
  const { name, reading } = useEntityName(
    recordKind ?? "person",
    recordKind && route.id,
  );
  if (recordKind && route.id) {
    return name ?? (reading === "failed" ? t("ref.nameLoadFailed") : route.id);
  }
  if (unit) {
    return unit.name;
  }
  return resolveTitle(route.screen, navItem?.labelKey, t);
}
