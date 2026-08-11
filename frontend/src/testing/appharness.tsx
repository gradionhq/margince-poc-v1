import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { App } from "../App";
import { LocaleProvider } from "../i18n";

// The fixtures every App-level suite needs, in one place.
//
// Extracted because there were three verbatim copies of `memoryStorage()` and
// of the /me-only fetch stub in the tree, including the same
// `map.get(key) as string`. Three copies of a Storage shim is three places a
// jsdom or Node change has to be chased, and the cast is the kind of thing that
// gets copied forward without being re-read.
//
// It is NOT a *.test.* file, on purpose: the design-system and lint gates skip
// test files, and a helper that renders the real App should answer to the same
// rules the app does.

/**
 * A `Storage` backed by a Map.
 *
 * Node ≥23 ships a global `localStorage` stub that jsdom does not replace, so a
 * suite reading the workspace slug needs its own. `getItem` returns `null` for
 * a miss — the Storage contract, and the reason the lookup is written as a
 * `has`/`get` pair rather than `get() ?? null`: a stored empty string is a
 * present value and must not read as absent.
 */
export function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (key) => {
      const value = map.get(key);
      return value === undefined ? null : value;
    },
    setItem: (key, value) => {
      map.set(key, String(value));
    },
    removeItem: (key) => {
      map.delete(key);
    },
    clear: () => map.clear(),
    key: (index) => Array.from(map.keys())[index] ?? null,
    get length() {
      return map.size;
    },
  };
}

/**
 * A `fetch` that answers the session probe and nothing else.
 *
 * Every other call gets a 503 so each screen falls to its own QueryGate error
 * state. That is deliberate for a routing test: the shell must render from the
 * session alone, and a stub that answered everything would hide a screen that
 * silently depends on a call it should not need.
 */
export function sessionOnlyFetch() {
  return async (input: Request | string | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return new Response(JSON.stringify({ user: {}, roles: [], teams: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify({ code: "unavailable" }), {
      status: 503,
      headers: { "Content-Type": "application/problem+json" },
    });
  };
}

/**
 * Render the real `App` under a fresh QueryClient and the locale provider.
 *
 * A fresh client per mount, never a shared one: react-query caches by key, and
 * a client carried between tests would serve one test's /me to the next.
 */
export function renderApp(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <App />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}
