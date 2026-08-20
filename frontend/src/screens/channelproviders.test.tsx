/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { components } from "../api/schema";
import { useProviderLabel } from "./channelproviders";

// The directory names a transport for a human. What matters is not that it
// looks the label up — it is what it does when it CANNOT, because a provider
// this build has never heard of is exactly what an extension unit creates, and
// the timeline still has to render.

type Directory = components["schemas"]["ChannelProviderDirectory"];
type Entry = components["schemas"]["ChannelProviderEntry"];

// The wire, not the client, is the boundary worth standing in for: stubbing
// `api.GET` would let a fixture claim a body the contract does not describe,
// which is the one thing this suite reads the generated types to prevent.
function stubDirectory(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
}

// A transport carries files or it does not, and the directory says which for
// every entry. None of that bears on a label, so it is stated once here rather
// than in each case: a fixture that repeated it would invite the next reader to
// look for meaning in the numbers.
function transport(provider: string, label: string): Entry {
  return {
    provider,
    label,
    credential_model: "workspace_bot",
    supplies_transport: true,
    attachments: {
      carries: false,
      max_files: 0,
      max_bytes_per_file: 0,
      max_body_with_files: 0,
    },
  };
}

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useProviderLabel", () => {
  it("names a registered transport with its label", async () => {
    const directory: Directory = {
      data: [transport("telegram", "Telegram")],
    };
    stubDirectory(directory);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current("telegram")).toBe("Telegram"));
  });

  it("falls back to the raw id for a transport it has never heard of", async () => {
    const directory: Directory = { data: [] };
    stubDirectory(directory);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    // Not "Unknown", not empty: an id a human can read beats either, and the
    // directory is a resolver rather than a gate — a label it cannot supply
    // must never make the row unreadable.
    await waitFor(() => expect(result.current("dispact")).toBe("dispact"));
  });

  // The defect this closes: a unit's channel messages resolved to "Dispact"
  // while the same unit's mail — landed on no channel, so its connector is the
  // natural key's source system — sat beside them as the raw
  // `ext:dispact-connector:dispact`. One transport, two spellings, one of them
  // provenance nobody outside this repository can parse.
  it("names a unit's capture provenance, not just its transports", async () => {
    const directory: Directory = {
      data: [transport("dispact", "Dispact")],
      capture_sources: [
        { source: "ext:dispact-connector:dispact", label: "Dispact" },
      ],
    };
    stubDirectory(directory);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() =>
      expect(result.current("ext:dispact-connector:dispact")).toBe("Dispact"),
    );
    // Both spellings of the one transport now read the same, which is the point:
    // a member should not have to know a record arrived on a channel or not.
    expect(result.current("dispact")).toBe("Dispact");
  });

  // An installation composing no ingress unit answers without the key at all,
  // and that must read as "nothing to add", never as a directory to distrust.
  it("resolves transports normally when no unit publishes a capture source", async () => {
    const directory: Directory = {
      data: [transport("telegram", "Telegram")],
    };
    stubDirectory(directory);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current("telegram")).toBe("Telegram"));
    expect(result.current("ext:notes:notes")).toBe("ext:notes:notes");
  });

  it("falls back rather than throwing when the directory cannot be read", async () => {
    // A failed fetch is the same case as an unknown provider from the row's
    // point of view. The timeline must not go blank because a lookup for a
    // display string failed.
    stubDirectory({ title: "boom", status: 500 }, 500);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current("telegram")).toBe("telegram"));
  });
});
