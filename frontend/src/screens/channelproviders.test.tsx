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
