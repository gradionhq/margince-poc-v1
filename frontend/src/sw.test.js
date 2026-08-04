// The service worker registers its listeners as a side effect of being loaded
// (it is the SW global scope, never imported by the app bundle), so the only
// way to exercise it is to load it into a harness that stands in for that
// scope: `addEventListener` is intercepted to capture the handlers instead of
// registering them for real, and `caches`/`location` are stubbed. Each test
// then drives the captured `fetch` handler directly with a fake FetchEvent —
// the same shape the real scope hands it (`request`, `respondWith`).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

async function loadServiceWorker() {
  const listeners = {};
  vi.stubGlobal("addEventListener", (type, handler) => {
    listeners[type] = handler;
  });
  vi.stubGlobal("skipWaiting", vi.fn());
  vi.stubGlobal("clients", { claim: vi.fn() });
  vi.stubGlobal("location", { origin: "https://app.example.test" });
  vi.stubGlobal("caches", {
    open: vi.fn(),
    keys: vi.fn(),
    delete: vi.fn(),
    match: vi.fn(),
  });
  // resetModules drops sw.js from vitest's module cache, so this import
  // re-evaluates the file (and so re-registers its listeners) every time.
  vi.resetModules();
  await import("../public/sw.js");
  return listeners;
}

describe("service worker fetch handler", () => {
  let listeners;

  beforeEach(async () => {
    listeners = await loadServiceWorker();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never caches a URL that carries a query string", async () => {
    globalThis.caches.match.mockResolvedValue(undefined);
    const cachePut = vi.fn();
    globalThis.caches.open.mockResolvedValue({ put: cachePut });
    globalThis.fetch = vi.fn();

    let respondWithCalled = false;
    const event = {
      request: {
        method: "GET",
        url: "https://app.example.test/shell.js?token=secret",
      },
      respondWith: () => {
        respondWithCalled = true;
      },
    };
    listeners.fetch(event);

    // The guard returns before respondWith — the browser's own default fetch
    // handles the request untouched, so nothing this scope owns ever opens
    // Cache Storage for it.
    expect(respondWithCalled).toBe(false);
    expect(globalThis.caches.open).not.toHaveBeenCalled();
    expect(globalThis.fetch).not.toHaveBeenCalled();
    expect(cachePut).not.toHaveBeenCalled();
  });

  it("caches a same-origin, query-free GET on a successful fetch", async () => {
    globalThis.caches.match.mockResolvedValue(undefined);
    const cachePut = vi.fn();
    globalThis.caches.open.mockResolvedValue({ put: cachePut });
    const response = { ok: true, clone: vi.fn() };
    response.clone.mockReturnValue(response);
    globalThis.fetch = vi.fn().mockResolvedValue(response);

    let respondWithPromise;
    const request = { method: "GET", url: "https://app.example.test/" };
    const event = {
      request,
      respondWith: (promise) => {
        respondWithPromise = promise;
      },
    };
    listeners.fetch(event);
    await respondWithPromise;

    expect(globalThis.fetch).toHaveBeenCalledWith(request);
    expect(cachePut).toHaveBeenCalledWith(request, response);
  });

  it("leaves /v1 API traffic alone", () => {
    globalThis.fetch = vi.fn();
    let respondWithCalled = false;
    const event = {
      request: { method: "GET", url: "https://app.example.test/v1/deals" },
      respondWith: () => {
        respondWithCalled = true;
      },
    };
    listeners.fetch(event);

    expect(respondWithCalled).toBe(false);
    expect(globalThis.caches.open).not.toHaveBeenCalled();
  });

  it("leaves cross-origin requests alone", () => {
    let respondWithCalled = false;
    const event = {
      request: { method: "GET", url: "https://other.example.test/asset.js" },
      respondWith: () => {
        respondWithCalled = true;
      },
    };
    listeners.fetch(event);

    expect(respondWithCalled).toBe(false);
    expect(globalThis.caches.open).not.toHaveBeenCalled();
  });
});
