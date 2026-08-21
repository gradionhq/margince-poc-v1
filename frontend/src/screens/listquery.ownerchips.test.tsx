/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { en } from "../i18n/en";
import { useOwnerChips } from "./listquery";

// The owner dial's team options come from TWO reads that can disagree: the ids
// are the viewer's own memberships off /me, and the labels are the workspace
// roster. Building the options by filtering the ROSTER for those ids let the
// label's absence decide the option's — so a team the bounded walk never reached
// produced no dial at all, and the viewer silently lost a filter the API would
// have answered, with nothing on screen saying anything was missing.

const VIEWER = "00000000-0000-4000-8000-000000000001";

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * `/teams`, answered the way the contract answers it: a page and the cursor for
 * the next one. `lastPage` past every index is a server that never stops
 * offering another, which is what makes the walk stop at its bound and report
 * the list as part of one.
 */
function stub(memberships: readonly string[], lastPage: number) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      if (url.pathname.endsWith("/me")) {
        return json({ ...meFixture(), teams: memberships });
      }
      const cursor = url.searchParams.get("cursor");
      const index = cursor ? Number(cursor) : 0;
      const last = index >= lastPage;
      return json({
        data: [
          index === 0
            ? { id: "tm-known", name: "Region West" }
            : { id: `tm-other-${index}`, name: `Region ${index}` },
        ],
        page: {
          next_cursor: last ? null : String(index + 1),
          has_more: !last,
        },
      });
    }),
  );
}

function wrapper({ children }: Readonly<{ children: ReactNode }>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

beforeEach(() => localStorage.setItem("margince.workspaceSlug", "acme"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the owner dial's team options", () => {
  it("keeps a dial for a team the walk never reached, and says the name did not load", async () => {
    stub(["tm-known", "tm-far"], Number.POSITIVE_INFINITY);

    const { result } = renderHook(() => useOwnerChips(), { wrapper });

    const labelFor = (value: string) =>
      (result.current[0]?.options ?? []).find(
        (option) => option.value === value,
      )?.label;

    // The dial survives, because the API answers it whether or not this walk
    // read the team's name — and it says which of the two it is short of rather
    // than printing a uuid nobody can read.
    await waitFor(() =>
      expect(labelFor("owner_team_id:tm-far")).toBe(en["ref.nameLoadFailed"]),
    );
    expect(labelFor("owner_team_id:tm-known")).toBe("Region West");
  });

  it("drops a dial the finished roster has answered about", async () => {
    stub(["tm-known", "tm-archived"], 0);

    const { result } = renderHook(() => useOwnerChips(), { wrapper });

    await waitFor(() =>
      expect(
        (result.current[0]?.options ?? []).some(
          (option) => option.label === "Region West",
        ),
      ).toBe(true),
    );
    // A walk that reached the end HAS answered about this id: `/teams` excludes
    // archived teams, so it is not one this reader may list, and an option whose
    // only honest label is a uuid is worse than one dial fewer.
    expect(
      (result.current[0]?.options ?? []).map((option) => option.value),
    ).toEqual([
      `owner_id:${VIEWER}`,
      "owner_team_id:tm-known",
      "unassigned:true",
    ]);
  });
});
