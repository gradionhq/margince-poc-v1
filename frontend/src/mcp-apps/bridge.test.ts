// @vitest-environment jsdom
//
// A view is a document, not a Node module: this spec drives a real DOM —
// postMessage, createElement, textContent — so it declares jsdom for itself
// rather than moving the whole suite off the cheaper node environment. Per file
// rather than by config glob, because `environmentMatchGlobs` is deprecated and
// warns on every run of the suite.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const PROTOCOL_VERSION = "2026-01-26";

type Sent = { msg: Record<string, unknown>; target: string };

// Each test re-imports the module so the handshake runs fresh against a new
// parent stub — the bridge announces itself at import time, exactly as it does
// inside a host, so there is no other way to observe the opening message.
//
// The listeners it registers are TRACKED AND REMOVED afterwards. jsdom's window
// outlives the module registry that resetModules clears, so a previous
// instance's listener would still be attached, still read `window.parent`
// dynamically, and still answer the next test's handshake — producing a second
// `initialized` that the "does not re-announce" assertion would blame on the
// bridge.
const detach: Array<() => void> = [];

async function loadBridge(parent: Window) {
  vi.resetModules();
  Object.defineProperty(window, "parent", {
    value: parent,
    configurable: true,
  });
  const added: Array<[string, EventListenerOrEventListenerObject]> = [];
  // Bound before the spy replaces it: jsdom's window is a proxy whose event
  // methods brand-check their receiver, so reaching for EventTarget.prototype
  // instead throws.
  const attach = window.addEventListener.bind(window);
  const spy = vi
    .spyOn(window, "addEventListener")
    .mockImplementation((type, listener, options) => {
      added.push([type, listener]);
      attach(type, listener, options);
    });
  const bridge = await import("./bridge");
  spy.mockRestore();
  detach.push(() => {
    for (const [type, listener] of added)
      window.removeEventListener(type, listener);
  });
  return bridge;
}

function stubParent(): { win: Window; sent: Sent[] } {
  const sent: Sent[] = [];
  const win = {
    postMessage: (msg: Record<string, unknown>, target: string) => {
      sent.push({ msg, target });
    },
  } as unknown as Window;
  return { win, sent };
}

// jsdom has no matchMedia at all, so the platform arm needs one supplied. It
// answers a live list rather than a frozen boolean: the bridge subscribes to the
// change event, and a stub that could not fire it would leave that arm untested.
function stubDarkPreference(matches: boolean): { flip: (to: boolean) => void } {
  const listeners = new Set<() => void>();
  const query = {
    matches,
    addEventListener: (_type: string, listener: () => void) => {
      listeners.add(listener);
    },
    removeEventListener: (_type: string, listener: () => void) => {
      listeners.delete(listener);
    },
  };
  vi.stubGlobal("matchMedia", (media: string) =>
    media.includes("dark") ? query : { ...query, matches: false },
  );
  return {
    flip: (to: boolean) => {
      query.matches = to;
      for (const listener of listeners) listener();
    },
  };
}

function deliver(source: Window, origin: string, data: unknown) {
  window.dispatchEvent(
    new MessageEvent("message", {
      source: source as MessageEventSource,
      origin,
      data,
    }),
  );
}

beforeEach(() => {
  const root = document.createElement("main");
  root.id = "root";
  document.body.replaceChildren(root);
  document.documentElement.removeAttribute("data-theme");
});

afterEach(() => {
  for (const off of detach.splice(0)) off();
  vi.unstubAllGlobals();
});

describe("the bridge announces itself", () => {
  it("sends ui/initialize to a wildcard target, because the host origin is not yet known", async () => {
    const parent = stubParent();
    await loadBridge(parent.win);
    expect(parent.sent).toHaveLength(1);
    expect(parent.sent[0].msg.method).toBe("ui/initialize");
    expect(parent.sent[0].target).toBe("*");
    expect(
      (parent.sent[0].msg.params as { protocolVersion: string })
        .protocolVersion,
    ).toBe(PROTOCOL_VERSION);
  });

  it("pins the host origin from the initialize response and sends every later message to it", async () => {
    const parent = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    expect(parent.sent[1].msg.method).toBe("ui/notifications/initialized");
    expect(parent.sent[1].target).toBe("https://host.example");
  });

  it("completes the handshake when result is null, which is a legal JSON-RPC response", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: null,
    });
    const seen: unknown[] = [];
    bridge.onResult((data) => seen.push(data));
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { structuredContent: { data: { ok: true }, warnings: [] } },
    });
    expect(seen).toEqual([{ ok: true }]);
  });

  it("does not re-announce when the initialize response arrives twice", async () => {
    const parent = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    expect(
      parent.sent.filter(
        (s) => s.msg.method === "ui/notifications/initialized",
      ),
    ).toHaveLength(1);
  });

  it("follows the theme the host states, so the view is drawn the way its host is", async () => {
    const parent = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: { hostContext: { theme: "dark" } },
    });
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("falls back to the platform preference when the host states no theme", async () => {
    // The failure this is here for: the design tokens answer only
    // [data-theme="dark"], so a view left unstamped renders permanently light
    // inside a dark host.
    stubDarkPreference(true);
    const parent = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("follows a platform appearance change, but only where the host delegated one", async () => {
    const platform = stubDarkPreference(false);
    const parent = stubParent();
    await loadBridge(parent.win);
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id: parent.sent[0].msg.id,
      result: {},
    });
    expect(document.documentElement.dataset.theme).toBe("light");
    platform.flip(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("does not second-guess a host that stated a theme when the platform changes", async () => {
    const platform = stubDarkPreference(false);
    const parent = stubParent();
    await loadBridge(parent.win);
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id: parent.sent[0].msg.id,
      result: { hostContext: { theme: "light" } },
    });
    platform.flip(true);
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});

describe("the bridge refuses what it must", () => {
  it("ignores a message whose source is not the embedding frame", async () => {
    const parent = stubParent();
    const other = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(other.win, "https://attacker.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    expect(parent.sent).toHaveLength(1);
  });

  it("refuses a later message from an origin other than the pinned host", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    const seen: unknown[] = [];
    bridge.onResult((data) => seen.push(data));
    deliver(parent.win, "https://attacker.example", {
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { structuredContent: { data: { ok: true } } },
    });
    expect(seen).toEqual([]);
  });

  it("drops a tool result that arrives before the handshake completes", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const seen: unknown[] = [];
    bridge.onResult((data) => seen.push(data));
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { structuredContent: { data: { ok: true } } },
    });
    expect(seen).toEqual([]);
  });

  it("ignores a message that is not JSON-RPC at all", async () => {
    const parent = stubParent();
    await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", { id, result: {} });
    expect(parent.sent).toHaveLength(1);
  });

  it("passes the envelope's warnings through, so a bounded read cannot look complete", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    let got: unknown[] = [];
    bridge.onResult((_data, warnings) => {
      got = warnings;
    });
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: {
        structuredContent: { data: {}, warnings: [{ code: "truncated" }] },
      },
    });
    expect(got).toEqual([{ code: "truncated" }]);
  });

  it("drops a warning entry that carries no code, because a view asks by code", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const id = parent.sent[0].msg.id;
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      id,
      result: {},
    });
    let got: unknown[] = [];
    bridge.onResult((_data, warnings) => {
      got = warnings;
    });
    deliver(parent.win, "https://host.example", {
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: {
        structuredContent: {
          data: {},
          warnings: ["truncated", { code: "capped" }],
        },
      },
    });
    expect(got).toEqual([{ code: "capped" }]);
  });
});

describe("the render helpers refuse to render nonsense", () => {
  it("renders a non-finite proportion as an em dash rather than NaN", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    expect(bridge.percent(0.42)).toBe("42%");
    expect(bridge.percent(undefined)).toBe("—");
    expect(bridge.percent(Number.POSITIVE_INFINITY)).toBe("—");
    expect(bridge.count(Number.NaN)).toBe("—");
    expect(bridge.count(7)).toBe("7");
  });

  it("puts text on the page as text, never as markup", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    const node = bridge.el("span", "name", "<img>");
    expect(node.textContent).toBe("<img>");
    expect(node.children).toHaveLength(0);
  });

  it("reports one raised condition by code and ignores the rest", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    expect(
      bridge.warned([{ code: "sweep_truncated" }], "sweep_truncated"),
    ).toBe(true);
    expect(bridge.warned([{ code: "other" }], "sweep_truncated")).toBe(false);
    expect(bridge.warned([], "sweep_truncated")).toBe(false);
  });
});

describe("money is scaled by the currency's own minor units", () => {
  it("scales a two-decimal currency by a hundred", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    // 24_000_000 minor EUR is 240,000 — not 24,000,000, which is the mistake
    // that made an account brief report every deal a hundred times too large.
    expect(bridge.money(24_000_000, "EUR")).toContain("240,000");
  });

  it("does not scale a zero-decimal currency at all", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    // JPY stores 1234 minor units and means ¥1,234. A hard-coded /100 renders
    // ¥12.34 here and would pass every euro test in the suite.
    const yen = bridge.money(1234, "JPY");
    expect(yen).toContain("1,234");
    expect(yen).not.toContain("12.34");
  });

  it("renders an absent amount or currency as an em dash, never as zero", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    expect(bridge.money(undefined, "EUR")).toBe("—");
    expect(bridge.money(1000, undefined)).toBe("—");
    expect(bridge.money(Number.NaN, "EUR")).toBe("—");
  });

  it("renders a currency Intl does not know as an em dash rather than throwing", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    // Intl throws a RangeError on an unknown code, and a view that threw
    // mid-render would leave the reader a blank panel with nothing saying why.
    expect(() => bridge.money(1000, "not-a-currency")).not.toThrow();
    expect(bridge.money(1000, "not-a-currency")).toBe("—");
  });
});

describe("day renders the calendar day the server judged in", () => {
  it("renders an instant as its UTC day", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    expect(bridge.day("2026-06-10T12:00:00Z")).toBe("2026-06-10");
  });

  it("does not shift the day into the reader's own zone", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    // 23:30 UTC is already the next day in much of the world. Rendering it
    // there would put "due 2026-06-11" beside the word "overdue", which the
    // server judged against 2026-06-10.
    expect(bridge.day("2026-06-10T23:30:00Z")).toBe("2026-06-10");
  });

  it("renders an absent or unparseable instant as an em dash", async () => {
    const parent = stubParent();
    const bridge = await loadBridge(parent.win);
    expect(bridge.day(undefined)).toBe("—");
    expect(bridge.day("")).toBe("—");
    expect(bridge.day("not a date")).toBe("—");
  });
});
