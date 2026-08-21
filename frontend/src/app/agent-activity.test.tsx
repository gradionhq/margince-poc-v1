/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { useAgentActivity } from "./agent-activity";

// What the rail asks the runner, and how often. Three things here can only be
// wrong invisibly, which is why each has its own case: a poll left running for
// a tab nobody is looking at, a cached `running` row that keeps the status light
// on after the run has finished, and an unanswered read reported as "at rest"
// rather than as absent.

type ActivityItem = components["schemas"]["ActivityItem"];
type AgentActivity = components["schemas"]["AgentActivity"];

// FE-PARAM-1's window, mirrored here on purpose: the app's client serves a
// cached body for this long, so a mount-time refetch alone cannot explain a
// read that lands the moment the tab returns.
const STALE_TIME_MS = 30_000;

const A_RUN: ActivityItem = {
  id: "3f1c0a2e-0000-4000-8000-000000000001",
  kind: "morning_brief",
  state: "running",
  started_at: "2026-08-21T05:00:00Z",
};

function activity(running: readonly ActivityItem[]): AgentActivity {
  return { as_of: "2026-08-21T05:00:01Z", running: [...running], recent: [] };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** Whether the tab is hidden, read by the hook through `document.hidden`. */
let hidden = false;

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: STALE_TIME_MS } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

/**
 * Mounts the hook against a counted fetch stub.
 *
 * `answer` is called once per read so a test can count reads and change what
 * the runner says between them.
 */
function mount(answer: () => Response | Promise<Response>) {
  const reads: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      reads.push(new URL(request.url).pathname);
      return answer();
    }),
  );
  const { result, unmount } = renderHook(() => useAgentActivity(), { wrapper });
  return { result, unmount, reads };
}

/** Lets every timer up to `ms` fire, and every promise they start settle. */
async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** The browser event the hook listens for, with the flag it reads set first. */
async function setTabHidden(value: boolean): Promise<void> {
  hidden = value;
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeEach(() => {
  hidden = false;
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  // The listener-registry case spies on document itself, which would otherwise
  // outlive its test.
  vi.restoreAllMocks();
  Reflect.deleteProperty(document, "hidden");
});

describe("the cadence", () => {
  it("polls fast while a run is live", async () => {
    const { result, reads } = mount(() => jsonResponse(activity([A_RUN])));

    await advance(0);
    expect(reads).toEqual(["/v1/me/agent-activity"]);
    expect(result.current.working).toBe(true);

    await advance(3_000);
    expect(reads).toHaveLength(2);
  });

  it("polls slowly when nothing is running", async () => {
    const { result, reads } = mount(() => jsonResponse(activity([])));

    await advance(0);
    expect(reads).toHaveLength(1);
    expect(result.current.working).toBe(false);

    // The live cadence has passed several times over and asked nothing.
    await advance(29_000);
    expect(reads).toHaveLength(1);

    await advance(1_000);
    expect(reads).toHaveLength(2);
  });
});

describe("the visibility pause", () => {
  it("stops polling while the tab is hidden", async () => {
    const { reads } = mount(() => jsonResponse(activity([A_RUN])));
    await advance(0);
    expect(reads).toHaveLength(1);

    await setTabHidden(true);
    await advance(60_000);

    expect(reads).toHaveLength(1);
  });

  it("refetches the moment the tab comes back, without waiting for the interval", async () => {
    let running: readonly ActivityItem[] = [A_RUN];
    const { result, reads } = mount(() => jsonResponse(activity(running)));
    await advance(0);
    expect(result.current.working).toBe(true);

    await setTabHidden(true);
    // The run finishes while nobody is looking, so the cached body the query
    // still holds is the one thing that could keep the light on.
    running = [];
    await setTabHidden(false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    // No interval has elapsed and the cached body is still inside FE-PARAM-1's
    // window, so this second read exists only because returning to the tab
    // asked for it.
    expect(reads).toHaveLength(2);
    expect(result.current.working).toBe(false);
    expect(result.current.running).toEqual([]);
  });

  it("releases the very listener it registered, when the caller unmounts", async () => {
    // Asserted against the listener registry rather than against a read count,
    // because a read count cannot see this leak: the query unsubscribes its own
    // observer on unmount, so a listener left behind fetches nothing extra and
    // looks exactly like a listener that was removed. What it does do is keep
    // this hook's state setter alive across every route change for the life of
    // the document, and the registry is where that is visible.
    const added = vi.spyOn(document, "addEventListener");
    const removed = vi.spyOn(document, "removeEventListener");
    const { unmount } = mount(() => jsonResponse(activity([A_RUN])));
    await advance(0);

    const registered = added.mock.calls
      .filter(([type]) => type === "visibilitychange")
      .map(([, listener]) => listener);
    expect(registered).not.toHaveLength(0);

    unmount();

    // toContain compares functions by identity, so removing SOME other
    // listener, or a fresh closure with the same body, does not satisfy this.
    const released = removed.mock.calls
      .filter(([type]) => type === "visibilitychange")
      .map(([, listener]) => listener);
    for (const listener of registered) {
      expect(released).toContain(listener);
    }
  });
});

describe("a read that has not answered", () => {
  it("reports not-working while the read is pending", async () => {
    const { result } = mount(() => new Promise<Response>(() => {}));

    await advance(0);

    expect(result.current.working).toBe(false);
    expect(result.current.running).toEqual([]);
    expect(result.current.recent).toEqual([]);
  });

  it("reports not-working when the read fails", async () => {
    const { result } = mount(() =>
      jsonResponse({ title: "unavailable", status: 503 }, 503),
    );

    await advance(0);

    expect(result.current.working).toBe(false);
    expect(result.current.running).toEqual([]);
  });
});
