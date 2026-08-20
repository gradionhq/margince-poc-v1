/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { boundedReference, useReferenceOptions } from "./filterreference";

// Each target reads a different surface and a different display column — a seat
// is `display_name`, everything else is `name` — so the arms are tested one by
// one rather than in aggregate. Getting one wrong does not fail loudly: the
// dropdown renders the right NUMBER of options, each with a blank label, which
// reads as a broken control rather than as a mapping mistake.

afterEach(cleanup);

/** Every request made, so a test asserts which surface answered the target. */
function harness(body: unknown) {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      seen.push(String(input instanceof Request ? input.url : input));
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { seen, wrapper };
}

const page = { next_cursor: null, has_more: false };

describe("which surface answers a target", () => {
  it.each([
    ["tag", "/tags", { data: [{ id: "t-1", name: "VIP" }], page }, "VIP"],
    [
      "app_user",
      "/users",
      { data: [{ id: "u-1", display_name: "Ann Lee" }], page },
      // The one target whose label is NOT `name`. A seat mapped through `name`
      // would answer an option with no words in it.
      "Ann Lee",
    ],
    ["team", "/teams", { data: [{ id: "tm-1", name: "West" }], page }, "West"],
    [
      "pipeline",
      "/pipelines",
      { data: [{ id: "p-1", name: "New business" }], page },
      "New business",
    ],
    [
      "stage",
      "/stages",
      { data: [{ id: "s-1", name: "Qualified" }], page },
      "Qualified",
    ],
    [
      "project",
      "/projects",
      { data: [{ id: "pr-1", name: "Rollout" }], page },
      "Rollout",
    ],
  ] as const)(
    "%s reads %s and labels by the column it has",
    async (reference, path, body, label) => {
      const { seen, wrapper } = harness(body);
      const { result } = renderHook(() => useReferenceOptions(reference), {
        wrapper,
      });

      await waitFor(() => expect(result.current.options).toHaveLength(1));
      expect(result.current.options[0]?.label).toBe(label);
      expect(seen.some((url) => url.includes(path))).toBe(true);
    },
  );
});

describe("a target this module cannot enumerate", () => {
  it("asks for nothing and offers nothing for an organization", async () => {
    const { seen, wrapper } = harness({ data: [], page });
    const { result } = renderHook(() => useReferenceOptions("organization"), {
      wrapper,
    });

    // No request at all: an account list grows with the business, so the caller
    // renders a box instead. Firing the read and discarding it would spend a
    // request per clause to answer nothing.
    await waitFor(() => expect(result.current.loading).toBe(true));
    expect(seen).toHaveLength(0);
    expect(result.current.options).toEqual([]);
  });

  it("asks for nothing when the field names no target at all", async () => {
    const { seen, wrapper } = harness({ data: [], page });
    const { result } = renderHook(() => useReferenceOptions(undefined), {
      wrapper,
    });

    expect(seen).toHaveLength(0);
    expect(result.current.options).toEqual([]);
  });
});

describe("a read that fails", () => {
  it("reports the failure rather than an empty set", async () => {
    const seen: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        seen.push(String(input instanceof Request ? input.url : input));
        return new Response(
          JSON.stringify({ title: "Unavailable", status: 503 }),
          {
            status: 503,
            headers: { "Content-Type": "application/problem+json" },
          },
        );
      }),
    );
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);

    const { result } = renderHook(() => useReferenceOptions("tag"), {
      wrapper,
    });

    // `failed`, not merely empty. A caller that could only see zero options
    // would render a dropdown claiming this workspace has no tags — a confident
    // answer to a question the server never answered.
    await waitFor(() => expect(result.current.failed).toBe(true));
    expect(result.current.options).toEqual([]);
    expect(result.current.loading).toBe(false);
  });
});

describe("boundedReference", () => {
  it.each(["tag", "app_user", "team", "pipeline", "stage", "project"] as const)(
    "%s can be enumerated",
    (reference) => {
      expect(boundedReference(reference)).toBe(true);
    },
  );

  it("an organization cannot", () => {
    expect(boundedReference("organization")).toBe(false);
  });

  it("nor can a field that names no target", () => {
    expect(boundedReference(undefined)).toBe(false);
  });
});
