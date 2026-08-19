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
// Two members are also spelled as a named constant elsewhere, because the module
// that owns the route owns the name: `ext` is app/extensions.ts's
// EXTENSION_SCREEN and `reset-password` is screens/auth.tsx's RESET_ROUTE. Both
// constants are literal types, so a rename there stops compiling here rather
// than drifting.
const SCREENS = [
  "home",
  "contacts",
  "companies",
  "partners",
  "leads",
  "deals",
  "tasks",
  "inbox",
  "reports",
  "ai",
  "settings",
  "dedupe",
  "filters",
  "offers",
  "search",
  "share",
  "onboarding",
  "client",
  "book",
  "preferences",
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

export function navigate(route: Route): void {
  globalThis.location.hash = routeHash(route);
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
