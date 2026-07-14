// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { LocaleProvider } from "../i18n";

// Shared Storybook rendering harness for the screens/* modules (fe-uat
// render gate, frontend/scripts/fe-uat.mjs): every screen component reads
// through the openapi-fetch `api` client (global fetch) and expects a
// react-query + LocaleProvider context. Mirrors the *.test.tsx fetch-stub
// convention exactly, so a story renders off the same fixture shapes the
// unit tests already exercise — never a live network call.

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export const emptyPage = {
  data: [],
  page: { next_cursor: null, has_more: false },
};

// Maps "METHOD /path" (contract path, sans the /v1 prefix) to a canned
// response (or a pending promise, for a Pending-state story). A route not in
// the map falls back to an empty list page — the honest default for any GET
// a story doesn't care about — rather than a silent 404 that would render a
// confusing error state.
export type RouteMap = Record<
  string,
  (body: unknown) => Response | Promise<Response>
>;

// Installs the fetch stub synchronously — called from a story's `render()`,
// which runs before any component mount effects, so the first queryFn call
// always sees the stub in place (same ordering the RTL tests rely on).
export function installFetchStub(
  routes: RouteMap,
  fallback: () => Response = () => jsonResponse(emptyPage),
): void {
  globalThis.fetch = async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const request = input instanceof Request ? input : null;
    const url = new URL(
      request ? request.url : String(input),
      "https://storybook.local",
    );
    const method = request?.method ?? init?.method ?? "GET";
    const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
    let body: unknown = null;
    if (method !== "GET") {
      try {
        body = request ? await request.json() : JSON.parse(String(init?.body));
      } catch {
        body = null;
      }
    }
    const handler = routes[key];
    return handler ? handler(body) : fallback();
  };
}

// A fresh QueryClient (retry:false — a mocked 4xx/5xx settles immediately
// instead of react-query's default backoff, which would blow past fe-uat's
// render timeout) + LocaleProvider, the two contexts every screen needs.
export function StoryProviders({
  children,
}: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}
