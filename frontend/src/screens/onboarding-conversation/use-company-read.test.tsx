/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { initialConversationState } from "./conversation-machine";
import { safeStartError, useCompanyRead } from "./use-company-read";

// startRead's mutationFn throws ONE classified error (the site-reads
// endpoint's own RFC 7807 detail/title) and lets everything else — a
// network TypeError, a bug elsewhere — escape unclassified. safeStartError
// is the one place that distinction is read back: the gate and the thread
// both go through it rather than `startRead.error.message` directly, so
// neither can regress into showing a raw exception or an unfiltered server
// body.

function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function renderRead() {
  const machine = { current: initialConversationState };
  return renderHook(
    () =>
      useCompanyRead({
        dispatch: vi.fn(),
        machine,
        setDraft: vi.fn(),
        setSelectedFactKeys: vi.fn(),
        answers: [],
        onReadStarted: vi.fn(),
        proposalJoin: "ready",
      }),
    { wrapper },
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("safeStartError", () => {
  it("passes through the server's own detail for a classified start failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        Response.json(
          { title: "invalid_url", detail: "That address is not reachable." },
          { status: 422 },
        ),
      ),
    );
    const { result } = renderRead();

    act(() => {
      result.current.startRead.mutate("https://example.com");
    });

    await waitFor(() => expect(result.current.startRead.isError).toBe(true));
    expect(safeStartError(result.current.startRead.error)).toBe(
      "That address is not reachable.",
    );
  });

  it("never surfaces a raw exception when the request itself fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
    const { result } = renderRead();

    act(() => {
      result.current.startRead.mutate("https://example.com");
    });

    await waitFor(() => expect(result.current.startRead.isError).toBe(true));
    // The reader gets nothing to fill {detail} with — never "Failed to fetch".
    expect(safeStartError(result.current.startRead.error)).toBe("");
    expect(errorLog).toHaveBeenCalled();
  });
});
