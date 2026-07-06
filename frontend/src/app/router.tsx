import { useSyncExternalStore } from "react";

// Hash routing, like the prototype: "#/deals/01J9ZK" → { screen: "deals",
// id: "01J9ZK" }. Client routes live behind '#' so the embedded static build
// needs no server-side fallback (backend/web serves real files only).

export type Route = {
  screen: string;
  id?: string;
};

export function parseHash(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  if (parts.length === 0) {
    return { screen: "home" };
  }
  return { screen: parts[0], id: parts[1] };
}

export function routeHash(route: Route): string {
  return route.id ? `#/${route.screen}/${route.id}` : `#/${route.screen}`;
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
