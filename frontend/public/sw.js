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
