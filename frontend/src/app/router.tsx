import { useSyncExternalStore } from "react";

// Hash routing: "#/deals/01J9ZK" → { screen: "deals", id: "01J9ZK" }.
// Client routes live behind '#', so any static host serves index.html for
// every entry point — no server-side SPA fallback needed.

// Every address this product answers, spelled ONCE. The other places that
// enumerate destinations — the nav table and RAIL_LESS_SCREENS in app/nav.ts,
// the dispatch in App.tsx — are typed against `Screen` rather than repeating the
// names, so a destination cannot exist in one of them and be missing from the
// rest, and `navigate({ screen: "dealz" })` fails to compile instead of
// rendering a surface that reads as unbuilt.
//
// Three members are also spelled as a named constant elsewhere, because the
// module that owns the route owns the name: `ext` is app/extensions.ts's
// EXTENSION_SCREEN, `reset-password` is screens/auth.tsx's RESET_ROUTE, and
// `scheduled` is screens/scheduledsends.tsx's SCHEDULED_SCREEN. All three are
// literal types used where a `Screen` is expected, so a rename there stops
// compiling here rather than drifting.
const SCREENS = [
  "home",
  "contacts",
  "companies",
  "partners",
  "leads",
  "deals",
  "projects",
  "tasks",
  "inbox",
  "reports",
  "ai",
  "settings",
  "dedupe",
  "filters",
  "scheduled",
  "offers",
  "search",
  "share",
  "onboarding",
  "client",
  "book",
  "preferences",
  "room",
  "oauth-consent",
  "reset-password",
  "ext",
  "not-found",
] as const;

/**
 * The screen half of an address.
 *
 * Call sites name this alias and never the list above it, so the set of
 * addresses widens in one place.
 */
export type Screen = (typeof SCREENS)[number];

export type Route = {
  screen: Screen;
  id?: string;
  id2?: string;
  id3?: string;
};

const SCREEN_NAMES: ReadonlySet<string> = new Set(SCREENS);

// A type predicate, not a cast: the runtime test and the compile-time claim are
// the same expression, so there is no way for them to drift (the shape
// app/extensions.ts uses for unit RBAC objects).
export function isScreen(value: string): value is Screen {
  return SCREEN_NAMES.has(value);
}

export function parseHash(hash: string): Route {
  // A hash may carry a query of its own ("#/onboarding?utm=x"); the query is
  // not part of the route and must never leak into a screen name.
  const parts = hash
    .replace(/^#\/?/, "")
    .split("?")[0]
    .split("/")
    .filter(Boolean);
  if (parts.length === 0) {
    return { screen: "home" };
  }
  const [screen, id, id2, id3] = parts;
  if (!isScreen(screen)) {
    // A hash comes out of the URL bar, so its first segment is text a human
    // typed, not a Screen. An address this app does not answer is a page — the
    // not-found one — rather than a parse failure, and it carries no segments
    // below it: they addressed arguments of a screen that isn't there.
    return { screen: "not-found" };
  }
  return { screen, id, id2, id3 };
}

export function routeHash(route: Route): string {
  // The segments are positional and have no placeholder, so a gap ends the
  // path: an id3 with no id2 cannot be serialized without inventing one.
  const path: string[] = [];
  for (const segment of [route.screen, route.id, route.id2, route.id3]) {
    if (!segment) {
      break;
    }
    path.push(segment);
  }
  return `#/${path.join("/")}`;
}

/**
 * How many leading segments of an address identify WHAT is on screen.
 *
 * Anything beyond that depth chooses a VIEW of the same thing — a tab — and
 * moving between views must not throw the page away: the screen keeps its state,
 * the chrome above the tab strip keeps its DOM nodes, and the arrival animation
 * (design-system/enter.css) plays for the panel that actually changed instead of
 * for the header, the readings and the tab bar the reader just clicked.
 *
 * `WHOLE_ADDRESS` — every segment names the thing — is what almost every screen
 * wants, and the exceptions are only the screens that put a TAB in the URL. So
 * this table looks like it does nothing, and that is the point: it is the one
 * place a new tabbed screen declares itself, and a screen added to the union
 * cannot inherit an answer nobody chose, because `Record` makes the set
 * complete. Lower a screen's depth ONLY for a segment that selects a view of
 * what is already on screen — a flow step, an outcome, or a second record id is
 * a different thing, and remounting is right for those.
 */
const WHOLE_ADDRESS = 4;

const IDENTITY_DEPTH: Readonly<Record<Screen, number>> = {
  home: WHOLE_ADDRESS,
  // #/contacts/<person>/<tab> — the six person tabs are a view of one person,
  // and they are the reason this table exists.
  contacts: 2,
  companies: WHOLE_ADDRESS,
  partners: WHOLE_ADDRESS,
  leads: WHOLE_ADDRESS,
  deals: WHOLE_ADDRESS,
  projects: WHOLE_ADDRESS,
  tasks: WHOLE_ADDRESS,
  inbox: WHOLE_ADDRESS,
  reports: WHOLE_ADDRESS,
  ai: WHOLE_ADDRESS,
  // Not a tab, however much the sidebar looks like one: every settings entry is
  // its own page, and the admin half is a segment deeper.
  settings: WHOLE_ADDRESS,
  dedupe: WHOLE_ADDRESS,
  filters: WHOLE_ADDRESS,
  scheduled: WHOLE_ADDRESS,
  offers: WHOLE_ADDRESS,
  search: WHOLE_ADDRESS,
  share: WHOLE_ADDRESS,
  onboarding: WHOLE_ADDRESS,
  client: WHOLE_ADDRESS,
  book: WHOLE_ADDRESS,
  preferences: WHOLE_ADDRESS,
  room: WHOLE_ADDRESS,
  "oauth-consent": WHOLE_ADDRESS,
  "reset-password": WHOLE_ADDRESS,
  ext: WHOLE_ADDRESS,
  "not-found": WHOLE_ADDRESS,
};

/**
 * The address with its view-only segments dropped: what is on screen, not which
 * view of it.
 *
 * Built out of `routeHash` rather than joined by hand, so there is still one
 * spelling of an address in this module and a truncated one cannot drift from a
 * whole one.
 */
export function routeIdentity(route: Route): string {
  const depth = IDENTITY_DEPTH[route.screen];
  return routeHash({
    screen: route.screen,
    id: depth > 1 ? route.id : undefined,
    id2: depth > 2 ? route.id2 : undefined,
    id3: depth > 3 ? route.id3 : undefined,
  });
}

export function navigate(route: Route): void {
  globalThis.location.hash = routeHash(route);
}

/**
 * Go to an address WITHOUT leaving the current one in history.
 *
 * For a redirect, and only for a redirect: an address the product answers by
 * sending the reader somewhere else must not be a history entry, or Back lands
 * on it, it redirects again, and the reader cannot get out of the loop with the
 * one key that exists for getting out of things.
 *
 * `location.replace` rather than `history.replaceState`, because the hash IS
 * this router's state: `replaceState` leaves no `hashchange` behind, so the
 * store would keep serving the old address while the URL bar showed the new
 * one.
 */
export function navigateReplacing(route: Route): void {
  globalThis.location.replace(routeHash(route));
}

function subscribe(onChange: () => void): () => void {
  globalThis.addEventListener("hashchange", onChange);
  return () => globalThis.removeEventListener("hashchange", onChange);
}

export function useRoute(): Route {
  const hash = useSyncExternalStore(
    subscribe,
    () => globalThis.location.hash,
    () => "",
  );
  return parseHash(hash);
}
