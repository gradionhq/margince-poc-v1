/** @vitest-environment jsdom */
import { QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../app/queryclient";
import { translate } from "../../i18n";
import { initialConversationState } from "./conversation-machine";
import { safeStartError, useCompanyRead } from "./use-company-read";

// startRead's mutationFn throws ONE classified error (carrying the site-reads
// endpoint's own RFC 7807 body) and lets everything else — a network
// TypeError, a bug elsewhere — escape unclassified. safeStartError is the one
// place that distinction is read back: the gate and the thread both go
// through it rather than reading the caught value themselves, so neither can
// regress into showing a raw exception or an unfiltered server body.
//
// The application's OWN client, not a bare one: what reaches the console for
// each of those failures is half of what these cases pin, and that is the
// client's mutation sink's decision to make (app/queryclient.ts, FE-PARAM-4).

const t = (key: Parameters<typeof translate>[1]) => translate("en", key);

function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <QueryClientProvider client={createQueryClient()}>
      {children}
    </QueryClientProvider>
  );
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
  // Unconditionally, not at the end of the case that installed a spy: a case
  // that fails its assertion never reaches its own teardown, and a leaked
  // console spy would silently answer the next case's question for it.
  vi.restoreAllMocks();
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
    expect(safeStartError(result.current.startRead.error, t)).toBe(
      "That address is not reachable.",
    );
  });

  it("leaves {detail} empty when the refusal carried no words for a reader", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ status: 502 }, { status: 502 })),
    );
    const errorLog = vi.spyOn(console, "error").mockImplementation(() => {});
    const { result } = renderRead();

    act(() => {
      result.current.startRead.mutate("https://example.com");
    });

    await waitFor(() => expect(result.current.startRead.isError).toBe(true));
    // ob.gate.startFailed stands on its own; a developer placeholder in the
    // middle of it would read as the server's own guidance.
    expect(safeStartError(result.current.startRead.error, t)).toBe("");
    // Still the CLASSIFIED failure — a refusal the server made, not a client
    // bug — so nothing about it is written to the console.
    expect(errorLog).not.toHaveBeenCalled();
  });

  it("never surfaces a raw exception when the request itself fails, and reports it exactly once", async () => {
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
    expect(safeStartError(result.current.startRead.error, t)).toBe("");
    // ONE failure, ONE report, from the client's own sink. Reading the error
    // back is something the gate and the thread do on every render for as
    // long as it stands, so a report written from here would file the same
    // failure again for each of them.
    expect(safeStartError(result.current.startRead.error, t)).toBe("");
    expect(errorLog).toHaveBeenCalledTimes(1);
    expect(errorLog).toHaveBeenCalledWith(result.current.startRead.error);
  });
});
