/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import { useProviderLabel } from "./channelproviders";

// The directory names a transport for a human. What matters is not that it
// looks the label up — it is what it does when it CANNOT, because a provider
// this build has never heard of is exactly what an extension unit creates, and
// the timeline still has to render.

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("useProviderLabel", () => {
  it("names a registered transport with its label", async () => {
    vi.spyOn(api, "GET").mockResolvedValue({
      data: {
        data: [
          {
            provider: "telegram",
            label: "Telegram",
            credential_model: "workspace_bot",
            supplies_transport: true,
          },
        ],
      },
    } as never);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current("telegram")).toBe("Telegram"));
  });

  it("falls back to the raw id for a transport it has never heard of", async () => {
    vi.spyOn(api, "GET").mockResolvedValue({
      data: { data: [] },
    } as never);

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
    vi.spyOn(api, "GET").mockResolvedValue({
      data: {
        data: [
          {
            provider: "dispact",
            label: "Dispact",
            credential_model: "workspace_bot",
            supplies_transport: true,
          },
        ],
        capture_sources: [
          { source: "ext:dispact-connector:dispact", label: "Dispact" },
        ],
      },
    } as never);

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
    vi.spyOn(api, "GET").mockResolvedValue({
      data: {
        data: [
          {
            provider: "telegram",
            label: "Telegram",
            credential_model: "workspace_bot",
            supplies_transport: true,
          },
        ],
      },
    } as never);

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
    vi.spyOn(api, "GET").mockResolvedValue({
      error: { title: "boom", status: 500 },
    } as never);

    const { result } = renderHook(() => useProviderLabel(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current("telegram")).toBe("telegram"));
  });
});
