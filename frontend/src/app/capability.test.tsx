/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useMe } from "../screens/common";
import { type RbacAction, type RbacObject, useCan } from "./capability";
import { meFixture } from "./mefixture";

// useCan is the single place a permission question is answered, so every way
// it can be wrong is a whole class of wrong buttons. The cases below are the
// ones that would otherwise fail silently: an absent snapshot reading as
// permission, a grant on one object leaking to another, and a grant on one
// action leaking to its siblings.

function stubMe(body: unknown, status = 200) {
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

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

// Waits for /me to SETTLE before reading the answer. Waiting on the answer
// itself would be vacuous — useCan denies while loading, and `false` is a
// perfectly good boolean, so a naive wait would read the pre-fetch value and
// report every grant as absent.
async function can(object: RbacObject, action: RbacAction): Promise<boolean> {
  const { result } = renderHook(
    () => ({ me: useMe(), allowed: useCan(object, action) }),
    { wrapper },
  );
  await waitFor(() => {
    expect(result.current.me.isPending).toBe(false);
  });
  return result.current.allowed;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useCan", () => {
  it("grants exactly the action named, and none of its siblings", async () => {
    // Same object, one action granted. A screen that asked for `delete` when
    // it meant `update` — the custom-field archive trap — fails here.
    stubMe(meFixture({ allow: { custom_field: ["update"] } }));

    expect(await can("custom_field", "update")).toBe(true);
    expect(await can("custom_field", "create")).toBe(false);
    expect(await can("custom_field", "delete")).toBe(false);
    expect(await can("custom_field", "read")).toBe(false);
  });

  it("does not leak a grant across objects", async () => {
    // Divergence in the other axis. The seeded roles give admin and ops the
    // same posture on nearly everything, so a fixture that granted both
    // objects would pass even for a screen wired to the wrong one.
    stubMe(
      meFixture({
        allow: { automation: ["update"], webhook_subscription: ["read"] },
      }),
    );

    expect(await can("automation", "update")).toBe(true);
    expect(await can("webhook_subscription", "update")).toBe(false);
    expect(await can("webhook_subscription", "read")).toBe(true);
    expect(await can("automation", "read")).toBe(false);
  });

  it("denies an object the snapshot never mentions", async () => {
    // The server omits objects a role was never granted rather than sending
    // an all-false grant. An absent key must deny, not read as unrestricted.
    stubMe(meFixture({ allow: { automation: ["update"] } }));

    expect(await can("overlay_connection", "read")).toBe(false);
  });

  it("denies while /me is still loading", async () => {
    // A hook that answered `true` before the snapshot arrived would flash
    // every affordance on first paint and retract them a moment later.
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));

    const { result } = renderHook(() => useCan("automation", "update"), {
      wrapper,
    });

    expect(result.current).toBe(false);
  });

  it("denies when the server sends no authorization at all", async () => {
    // An older server, or one that failed to resolve the principal's grants.
    // Absence is not permission.
    const { authorization: _dropped, ...withoutAuthorization } = meFixture({
      allow: { automation: ["update"] },
    });
    stubMe(withoutAuthorization);

    expect(await can("automation", "update")).toBe(false);
  });

  it("denies when /me itself fails", async () => {
    stubMe({ title: "Unauthorized" }, 401);

    expect(await can("automation", "update")).toBe(false);
  });
});
