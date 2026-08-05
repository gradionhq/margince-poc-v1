/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useMe } from "../screens/common";
import {
  type RbacAction,
  type RbacObject,
  useCan,
  useCanMutate,
  useCanUpsert,
  useCanWrite,
  useHoldsWriteGrant,
} from "./capability";
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
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
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

// The seat answer, asked the same way — the licensing axis on its own.
async function mutate(): Promise<boolean> {
  const { result } = renderHook(
    () => ({ me: useMe(), allowed: useCanMutate() }),
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
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );

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

describe("useHoldsWriteGrant — any write verb", () => {
  // The question a nav entry into an authoring surface asks. Which write verb
  // a role holds varies by object — the seeded rep creates and updates a
  // product but never deletes one — so insisting on one verb would hide a page
  // its own cards would serve.
  async function holdsWrite(object: RbacObject): Promise<boolean> {
    const { result } = renderHook(
      () => ({ me: useMe(), allowed: useHoldsWriteGrant(object) }),
      { wrapper },
    );
    await waitFor(() => {
      expect(result.current.me.isPending).toBe(false);
    });
    return result.current.allowed;
  }

  it("accepts any one of create, update and delete", async () => {
    stubMe(
      meFixture({
        allow: {
          product: ["create"],
          offer_template: ["update"],
          pipeline: ["delete"],
        },
      }),
    );

    expect(await holdsWrite("product")).toBe(true);
    expect(await holdsWrite("offer_template")).toBe(true);
    expect(await holdsWrite("pipeline")).toBe(true);
  });

  it("refuses read — the verb every seeded role holds on nearly everything", async () => {
    // If read counted, this predicate would put every authoring surface in
    // front of every seat, which is the opposite of what it is for.
    stubMe(meFixture({ allow: { custom_field: ["read"] } }));

    expect(await holdsWrite("custom_field")).toBe(false);
    expect(await holdsWrite("fx_rate")).toBe(false);
  });

  it("ignores the licensing seat, so a read seat still reaches what it may read", async () => {
    // Deliberately unlike useCanWrite: the seat blocks the mutation, not the
    // page, and hiding the page would strand a reader on a fallback screen.
    stubMe(meFixture({ seat: "read", allow: { product: ["update"] } }));

    expect(await mutate()).toBe(false);
    expect(await holdsWrite("product")).toBe(true);
  });
});

describe("useCanUpsert — the grant an upsert control can honestly ask for", () => {
  // The rate sheets are one endpoint that inserts OR replaces, so the server
  // admits on create-or-update and demands the specific verb inside the
  // transaction. A control asking for `create` alone would hide the editor
  // from a principal the server would have admitted.
  async function upsert(object: RbacObject): Promise<boolean> {
    const { result } = renderHook(
      () => ({ me: useMe(), allowed: useCanUpsert(object) }),
      { wrapper },
    );
    await waitFor(() => {
      expect(result.current.me.isPending).toBe(false);
    });
    return result.current.allowed;
  }

  it("admits a principal holding only update, which create alone would hide", async () => {
    stubMe(meFixture({ allow: { fx_rate: ["update"] } }));

    expect(await upsert("fx_rate")).toBe(true);
  });

  it("admits a principal holding only create", async () => {
    stubMe(meFixture({ allow: { ai_model_rate: ["create"] } }));

    expect(await upsert("ai_model_rate")).toBe(true);
  });

  it("refuses a principal holding neither, however much else it reads", async () => {
    stubMe(
      meFixture({ allow: { fx_rate: ["read"], ai_model_rate: ["read"] } }),
    );

    expect(await upsert("fx_rate")).toBe(false);
    expect(await upsert("ai_model_rate")).toBe(false);
  });

  it("still clamps on the licensing seat — unlike the nav predicate", async () => {
    // This one DOES fold the seat in: it gates a mutating control, not a page.
    stubMe(
      meFixture({ seat: "read", allow: { fx_rate: ["create", "update"] } }),
    );

    expect(await upsert("fx_rate")).toBe(false);
  });
});

describe("useCanMutate / useCanWrite — the licensing seat", () => {
  // The seat is a SECOND axis the server clamps on the HTTP method, before
  // RBAC. A grant alone is not permission to write, and folding the two into
  // one answer is wrong in both directions.
  async function write(object: RbacObject, action: RbacAction) {
    const { result } = renderHook(
      () => ({ me: useMe(), allowed: useCanWrite(object, action) }),
      { wrapper },
    );
    await waitFor(() => {
      expect(result.current.me.isPending).toBe(false);
    });
    return result.current.allowed;
  }

  it("permits mutation only on a full seat", async () => {
    stubMe(meFixture({ seat: "full" }));
    expect(await mutate()).toBe(true);
  });

  it("refuses a read seat even where the grant allows the action", async () => {
    stubMe(meFixture({ seat: "read", allow: { automation: ["update"] } }));

    // The grant is genuinely held — this is the seat denying, not RBAC.
    expect(await can("automation", "update")).toBe(true);
    expect(await mutate()).toBe(false);
    expect(await write("automation", "update")).toBe(false);
  });

  it("refuses a seat the snapshot never states", async () => {
    // Dropping a required field must not buy the ability to mutate.
    const me = meFixture({ allow: { automation: ["update"] } });
    const { seat_type: _dropped, ...authorization } = me.authorization ?? {
      seat_type: "full" as const,
      objects: {},
    };
    stubMe({ ...me, authorization });

    expect(await mutate()).toBe(false);
    expect(await write("automation", "update")).toBe(false);
  });

  it("refuses a seat it does not recognize", async () => {
    const me = meFixture({ allow: { automation: ["update"] } });
    stubMe({
      ...me,
      authorization: { ...me.authorization, seat_type: "enterprise" },
    });

    expect(await mutate()).toBe(false);
  });

  it("still requires the grant on a full seat", async () => {
    stubMe(meFixture({ seat: "full", allow: { automation: ["read"] } }));

    expect(await mutate()).toBe(true);
    expect(await write("automation", "update")).toBe(false);
  });
});
