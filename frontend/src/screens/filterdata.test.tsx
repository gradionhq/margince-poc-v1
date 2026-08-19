/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fieldLabel,
  groupFields,
  useFilterPreview,
  useFilterVocabulary,
  type VocabularyField,
} from "./filterdata";
import { newGroup, newLeaf } from "./segmentpredicate";

// These hooks exist to decide WHEN NOT TO ASK, so that is what the tests watch:
// which URLs were requested, and how many times. Asserting on rendered output
// would prove the query resolved and say nothing about the request it did or did
// not make — which is the whole behaviour.

afterEach(cleanup);

const VOCAB_BODY = {
  resource: "person",
  fields: [
    {
      name: "full_name",
      type: "text",
      operators: ["eq", "contains"],
      custom: false,
    },
  ],
};

function harness() {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      seen.push(String(input instanceof Request ? input.url : input));
      return new Response(
        JSON.stringify({
          ...VOCAB_BODY,
          match_count: 7,
          columns: ["id"],
          rows: [],
          truncated: false,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { seen, wrapper };
}

describe("the vocabulary read", () => {
  it("asks for the resource it was given", async () => {
    const { seen, wrapper } = harness();
    const { result } = renderHook(() => useFilterVocabulary("organization"), {
      wrapper,
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(seen.some((url) => url.includes("resource=organization"))).toBe(
      true,
    );
  });

  it("serves a second reader of the same resource from cache", async () => {
    const { seen, wrapper } = harness();
    const { result } = renderHook(
      () => {
        // Two hooks, one resource — a builder and a picker on the same screen.
        useFilterVocabulary("person");
        return useFilterVocabulary("person");
      },
      { wrapper },
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(
      seen.filter((url) => url.includes("/filters/vocabulary")),
    ).toHaveLength(1);
  });
});

describe("the preview read", () => {
  it("does not ask about an empty group", async () => {
    const { seen, wrapper } = harness();
    renderHook(() => useFilterPreview("person", newGroup("and")), { wrapper });

    // An empty group is refused as filter_shape_invalid, so asking spends a
    // request to be told what isComplete already knows.
    await waitFor(() => expect(seen).toHaveLength(0));
  });

  it("does not ask about a clause whose value is still empty", async () => {
    const { seen, wrapper } = harness();
    renderHook(
      () =>
        useFilterPreview(
          "person",
          newGroup("and", [newLeaf("full_name", "contains", "")]),
        ),
      { wrapper },
    );

    // The 422 this would earn names the field, which is correct of the server and
    // useless to somebody who has not finished typing.
    await waitFor(() => expect(seen).toHaveLength(0));
  });

  it("asks once a clause is complete", async () => {
    const { seen, wrapper } = harness();
    const { result } = renderHook(
      () =>
        useFilterPreview(
          "person",
          newGroup("and", [newLeaf("full_name", "contains", "ann")]),
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.match_count).toBe(7);
    expect(seen.filter((url) => url.includes("/filters/preview"))).toHaveLength(
      1,
    );
  });

  it("spends one request on two trees that encode the same filter", async () => {
    const { seen, wrapper } = harness();
    // Structurally identical, different node ids — which is exactly what an edit
    // that removes a clause and retypes it produces. Keying on the encoded tree
    // rather than on identity is what stops the second one costing a request to
    // learn the count did not move.
    const first = newGroup("and", [newLeaf("full_name", "contains", "ann")]);
    const second = newGroup("and", [newLeaf("full_name", "contains", "ann")]);
    expect(first.id).not.toBe(second.id);

    const { result, rerender } = renderHook(
      ({ tree }: { tree: ReturnType<typeof newGroup> }) =>
        useFilterPreview("person", tree),
      { wrapper, initialProps: { tree: first } },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    rerender({ tree: second });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(seen.filter((url) => url.includes("/filters/preview"))).toHaveLength(
      1,
    );
  });
});

describe("presenting a vocabulary", () => {
  const FIELDS: VocabularyField[] = [
    { name: "owner_id", type: "id", operators: ["eq"], custom: false },
    { name: "cf_tier", type: "text", operators: ["eq"], custom: true },
    { name: "full_name", type: "text", operators: ["eq"], custom: false },
  ];

  it("keeps core fields ahead of a workspace's own", () => {
    const { core, custom } = groupFields(FIELDS);
    expect(core.map((f) => f.name)).toEqual(["owner_id", "full_name"]);
    expect(custom.map((f) => f.name)).toEqual(["cf_tier"]);
  });

  it("reads a custom column as the admin's words, not the column's", () => {
    // cf_ is a Go-side convention a human never typed, and an underscore is a
    // column separator rather than something anybody wrote in a label.
    expect(
      fieldLabel({
        name: "cf_loyalty_tier",
        type: "text",
        operators: [],
        custom: true,
      }),
    ).toBe("loyalty tier");
    // A core field keeps its own name: it has no prefix to strip, and stripping
    // one that only LOOKS like a prefix would rename the field.
    expect(
      fieldLabel({
        name: "cf_score",
        type: "number",
        operators: [],
        custom: false,
      }),
    ).toBe("cf score");
  });
});
