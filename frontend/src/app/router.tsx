import { useSyncExternalStore } from "react";

// Hash routing: "#/deals/01J9ZK" → { screen: "deals", id: "01J9ZK" }.
// Client routes live behind '#', so any static host serves index.html for
// every entry point — no server-side SPA fallback needed.

export type Route = {
  screen: string;
  id?: string;
  id2?: string;
};

export function parseHash(hash: string): Route {
  // A hash may carry a query of its own ("#/onboarding?conv"); the query is
  // not part of the route and must never leak into a screen name.
  const parts = hash
    .replace(/^#\/?/, "")
    .split("?")[0]
    .split("/")
    .filter(Boolean);
  if (parts.length === 0) {
    return { screen: "home" };
  }
  return { screen: parts[0], id: parts[1], id2: parts[2] };
}

export function routeHash(route: Route): string {
  const base = `#/${route.screen}`;
  if (!route.id) {
    return base;
  }
  return route.id2 ? `${base}/${route.id}/${route.id2}` : `${base}/${route.id}`;
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
