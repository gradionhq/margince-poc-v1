/* Margince service worker (B-EP09.8) — conservative by design:
 *  - /v1 is NEVER cached and NEVER faked: API calls are network-only, so an
 *    offline mutation fails honestly (the UI shows its error state; nothing
 *    is shown as committed that did not commit — §4.7).
 *  - Static shell assets are cache-first with network fill, so the shell
 *    loads offline in a read-only, honestly-degraded state.
 */
const CACHE = "margince-shell-v1";

globalThis.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(["/", "/manifest.webmanifest"])),
  );
  globalThis.skipWaiting();
});

globalThis.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))),
      )
      .then(() => globalThis.clients.claim()),
  );
});

globalThis.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);

  // API traffic: network-only. No cache, no synthetic response — offline
  // reads and writes fail loudly and the app renders honest degradation.
  if (request.method !== "GET" || url.pathname.startsWith("/v1")) {
    return;
  }
  if (url.origin !== globalThis.location.origin) {
    return;
  }
  // NEVER cache a URL that carries a query. A Cache Storage key is the full
  // request URL, query included, so caching one writes whatever the query holds
  // to disk — where it survives until the cache name is bumped, and is then
  // served cache-first. That is a credential store, not a shell cache: the
  // password-reset link used to arrive as `?token=…`, and caching its navigation
  // silently undid the front-end's own effort to scrub the token from history.
  //
  // The reset link is a hash route now and a fragment never reaches a service
  // worker at all, so this is defence in depth rather than the only guard — but
  // the rule is worth keeping absolute: nothing here needs a query cached, and
  // the next query-bearing URL will not come with a security review attached.
  if (url.search !== "") {
    return;
  }

  event.respondWith(
    caches.match(request).then(
      (cached) =>
        cached ??
        fetch(request).then((response) => {
          if (response.ok) {
            const copy = response.clone();
            caches.open(CACHE).then((cache) => cache.put(request, copy));
          }
          return response;
        }),
    ),
  );
});
