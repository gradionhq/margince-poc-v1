// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAgentTicker } from "./agentrail-ticker";

// The line under the orb, as a unit. What it says is the product's own promise
// about this surface: it names what THIS reader's tab is doing, it names the
// record rather than the cache key, and it says nothing at all rather than
// guessing. Each case here is one of those promises.

function Ticker() {
  const lines = useAgentTicker();
  return <p data-testid="line">{lines[0]?.said ?? ""}</p>;
}

let client: QueryClient;

function harness(children: ReactNode) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

/** The line the surface is showing right now, or "" while it shows none. */
function line(): string {
  return screen.getByTestId("line").textContent ?? "";
}

/**
 * Drives one read to completion through the real cache.
 *
 * Through `fetchQuery` rather than a hand-built event: the ticker listens to the
 * cache's own notifications, and a hand-fired event would prove only that the
 * reducer works on the shape this test invented.
 */
async function read(key: readonly unknown[], data: unknown) {
  await act(async () => {
    await client.fetchQuery({ queryKey: [...key], queryFn: async () => data });
  });
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Number.POSITIVE_INFINITY },
    },
  });
});

afterEach(() => {
  cleanup();
  client.clear();
  vi.useRealTimers();
});

describe("useAgentTicker", () => {
  it("says nothing at all until something is read", () => {
    render(harness(<Ticker />));
    expect(line()).toBe("");
  });

  it("names a list read in the reader's own words, not the cache key's", async () => {
    render(harness(<Ticker />));
    await read(["deals"], { data: [] });
    expect(line()).toBe("Reading the pipeline");
  });

  it("says nothing for a read that is plumbing rather than work", async () => {
    // The session, the feature flags, the field catalog: a status line that
    // narrated those would bury the two or three events a reader cares about.
    render(harness(<Ticker />));
    await read(["me"], { id: "u-1" });
    expect(line()).toBe("");
  });

  it("names the record when the tab already knows what it is called", async () => {
    render(harness(<Ticker />));
    // The list the reader arrived from carried the name, which is the case that
    // lets the record read be named the moment it starts.
    await read(["organizations"], {
      data: [{ id: "o-1", name: "zenloop" }],
    });
    await read(["organization360", "o-1"], { id: "o-1", name: "zenloop" });
    expect(line()).toBe("Reading everything about zenloop");
  });

  it("says nothing for a record it cannot name yet, rather than naming its kind", async () => {
    // "Reading a company" tells a reader nothing they cannot see from the page
    // they are standing on, and printing it while the name is one moment away is
    // worse than waiting that moment.
    render(harness(<Ticker />));
    await act(async () => {
      client.getQueryCache().notify({
        type: "updated",
        query: client
          .getQueryCache()
          .build(client, { queryKey: ["organization360", "unknown-id"] }),
        action: { type: "fetch" },
      });
    });
    expect(line()).toBe("");
  });

  it("drops a line once its moment has passed", async () => {
    render(harness(<Ticker />));
    await read(["deals"], { data: [] });
    expect(line()).toBe("Reading the pipeline");
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(line()).toBe("");
  });

  it("reports the newest thing first when two reads land together", async () => {
    render(harness(<Ticker />));
    await read(["deals"], { data: [] });
    await read(["tasks"], { data: [] });
    expect(line()).toBe("Reading your tasks");
  });
});
