// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { type Locale, LocaleProvider } from "../i18n";

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

// The session probe is the ONE route the fallback must not answer, and the
// reason is not that an empty list is a poor guess: `useMe` rejects a body
// with no `user` as malformed, every capability hook then fails closed, and
// the screen quietly renders its denied branch. Nothing about that failure is
// visible — the card draws, the screenshot is taken, the render gate is green,
// and the story's NAME is the only thing still claiming what it shows. Two
// files' worth of stories sat in that state until somebody read the fixtures.
//
// There is also no correct guess available: answering with a full-grant
// fixture would make every unrouted story an admin, which is the same lie
// pointed the other way. So say so, loudly enough that the render gate marks
// the story (a console error fails a capture) instead of blessing it.
const SESSION_PROBE = "GET /me";

function unroutedSessionProbe(): Response {
  console.error(
    "story fetch stub: a component asked for GET /me and this story did not " +
      "route it. Add '\"GET /me\": () => jsonResponse(meFixture({ allow: … }))' " +
      "with the grants the story is about — the stub cannot guess them, and " +
      "the list-shaped fallback reads as a malformed session, which fails " +
      "every grant closed and renders a branch the story is not named for.",
  );
  return jsonResponse(
    {
      title: "GET /me was not routed by this story",
      status: 501,
      detail: "See the console error above; add the route to installFetchStub.",
    },
    501,
  );
}

/**
 * The session route, in one spelling: `"GET /me": meRoute({ … })`.
 *
 * Every story that mounts a role-aware surface needs this line, and the reason
 * it is a helper rather than four lines copied per file is that the copies
 * drifted into omission — which is invisible, because a missing session looks
 * exactly like a denied one on screen.
 *
 * `allow` is the grants the story is ABOUT, and it has to be spelled out rather
 * than defaulted into: `meRoute({})` says the story means it.
 *
 * What `meRoute({})` is NOT is a denied seat. `meFixture` defaults to
 * `roles: ["admin"]` on a full seat, so an empty `allow` is an ADMIN holding no
 * object grants — `useHoldsAdminRole()` is true and `useCanMutate()` is true,
 * while every `useCan()` is false. That is a real principal and a useful one,
 * but a story about a denial has to say so with the second argument:
 * `meRoute({}, { roles: ["rep"], seat: "read" })`. Getting this wrong is
 * invisible on screen, which is how a story ends up named for a boundary it
 * never draws.
 */
export function meRoute(
  allow: GrantSpec,
  identity: { roles?: string[]; seat?: "full" | "read" } = {},
): () => Response {
  return () => jsonResponse(meFixture({ ...identity, allow }));
}

// "METHOD /path", with the /v1 prefix stripped so a RouteMap key reads as the
// contract path. The base is a placeholder: a relative request URL needs one,
// and no story ever reaches a host.
function routeKey(
  input: RequestInfo | URL,
  request: Request | null,
  method: string,
): string {
  const url = new URL(
    request ? request.url : String(input),
    "https://storybook.local",
  );
  return `${method} ${url.pathname.replace(/^\/v1/, "")}`;
}

// A handler is passed the parsed write body so a story can echo the request
// back (a PATCH story's optimistic row). An unparseable body is null rather
// than a throw: what a story does with a malformed write is the story's call.
async function requestBody(
  request: Request | null,
  init: RequestInit | undefined,
  method: string,
): Promise<unknown> {
  if (method === "GET") return null;
  try {
    return request ? await request.json() : JSON.parse(String(init?.body));
  } catch {
    return null;
  }
}

// Installs the fetch stub synchronously — called from a story's `render()`,
// which runs before any component mount effects, so the first queryFn call
// always sees the stub in place (same ordering the RTL tests rely on).
// Marks the patched fetch so StoryProviders can tell "this story routed its
// requests" from "this story never called installFetchStub, and every request
// it makes is leaving for a real host".
const STUB_INSTALLED = Symbol.for("margince.storyFetchStub");

export function installFetchStub(
  routes: RouteMap,
  fallback: () => Response = () => jsonResponse(emptyPage),
): void {
  const stub = async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const request = input instanceof Request ? input : null;
    const method = request?.method ?? init?.method ?? "GET";
    const key = routeKey(input, request, method);
    const handler = routes[key];
    if (handler) {
      // A GET handler is invoked WITHOUT awaiting first. The await is not
      // free: it defers the call by a microtask, and a story or test that
      // captures a deferred resolver inside its handler then races the
      // assertion that resolves it.
      return method === "GET"
        ? handler(null)
        : handler(await requestBody(request, init, method));
    }
    return key === SESSION_PROBE ? unroutedSessionProbe() : fallback();
  };
  (stub as typeof fetch & { [STUB_INSTALLED]?: true })[STUB_INSTALLED] = true;
  globalThis.fetch = stub as typeof fetch;
}

// The half of the guard that installFetchStub cannot cover: a story that never
// calls it at all. Those requests do not hit a fallback — they leave the page
// for whatever host the iframe is served from, 404, and resolve to an empty
// answer that looks like a legitimate one. Two stories were screenshotting an
// anonymous avatar that way.
//
// So the providers every story already wraps with put a stub in place when none
// is, and that stub routes nothing: the session probe says so loudly, and
// everything else gets the same empty page the explicit fallback gives.
function ensureFetchStubInstalled(): void {
  const patched = globalThis.fetch as typeof fetch & {
    [STUB_INSTALLED]?: true;
  };
  if (!patched?.[STUB_INSTALLED]) {
    installFetchStub({});
  }
}

// A fresh QueryClient (retry:false — a mocked 4xx/5xx settles immediately
// instead of react-query's default backoff, which would blow past fe-uat's
// render timeout) + LocaleProvider, the two contexts every screen needs.
//
// `locale` is PINNED rather than detected — a catalog that renders in whatever
// language the reviewer's browser asks for compares against nothing. English is
// the default because that is what every existing story was written against;
// passing "de" is how a story reviews the German copy, whose length is the thing
// worth looking at (it runs 20-35% longer, and the layouts are built for that).
export function StoryProviders({
  children,
  locale = "en",
}: Readonly<{ children: ReactNode; locale?: Locale }>) {
  // Before the client, so a story that routed nothing cannot reach the network
  // on its first render.
  ensureFetchStubInstalled();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial={locale}>{children}</LocaleProvider>
    </QueryClientProvider>
  );
}
