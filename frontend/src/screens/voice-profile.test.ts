import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { ensureProfileId } from "./voice-profile";

// An owner has exactly ONE Voice DNA. Onboarding and the Settings card both
// offer to start it, so both can observe an empty list and both can mint —
// and the server's one-live-profile rule then refuses the loser. These tests
// hold the resolution to one request no matter how the two callers interleave,
// and hold the recovery honest when a create is refused anyway.

type VoiceProfile = components["schemas"]["VoiceProfile"];

const PROFILE: VoiceProfile = {
  id: "vp-1",
  owner_id: "u1",
  status: "collecting",
  maturity: "collecting",
  quality_band: "thin",
  voice_profile_md: "",
  profile_version: 0,
  personality_md: "",
  auto_learning_enabled: false,
  active_source_hash: null,
  candidate_version: null,
  last_built_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

// A promise whose settlement the test owns. Ordering between the two callers
// is established by these gates rather than by a timer: a race decided by a
// sleep is a race the test does not actually control.
function gate(): { reached: Promise<void>; open: () => void } {
  let opener: (() => void) | null = null;
  const reached = new Promise<void>((resolve) => {
    opener = resolve;
  });
  // The Promise executor runs synchronously, so the resolver is bound by now.
  if (opener === null) {
    throw new Error("the promise executor did not run");
  }
  const open = opener;
  return { reached, open };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const emptyList = { data: [], page: { next_cursor: null, has_more: false } };
const onePage = {
  data: [PROFILE],
  page: { next_cursor: null, has_more: false },
};

type Handlers = Readonly<{
  list: (call: number) => Promise<Response>;
  create: (call: number) => Promise<Response>;
}>;

// Records the calls each handler sees, so "exactly one create" is read off the
// wire rather than off a return value both callers could share by accident.
function stubApi(handlers: Handlers) {
  const calls: string[] = [];
  let lists = 0;
  let creates = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      calls.push(`${request.method} ${path}`);
      if (path === "/voice-profiles" && request.method === "POST") {
        creates += 1;
        return handlers.create(creates);
      }
      if (path === "/voice-profiles") {
        lists += 1;
        return handlers.list(lists);
      }
      throw new Error(`unstubbed request ${request.method} ${path}`);
    }),
  );
  return calls;
}

const countOf = (calls: readonly string[], call: string) =>
  calls.filter((made) => made === call).length;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ensureProfileId across the two surfaces that can start a voice", () => {
  it("mints once when both callers ask at the same moment", async () => {
    const calls = stubApi({
      list: async () => jsonResponse(emptyList),
      create: async () => jsonResponse(PROFILE, 201),
    });

    const [first, second] = await Promise.all([
      ensureProfileId(),
      ensureProfileId(),
    ]);

    expect(first).toBe("vp-1");
    expect(second).toBe("vp-1");
    expect(countOf(calls, "POST /voice-profiles")).toBe(1);
    expect(countOf(calls, "GET /voice-profiles")).toBe(1);
  });

  it("mints once when the second caller arrives while the first is minting", async () => {
    // The dangerous interleave: the first caller has already read an empty
    // list and its create is in flight, which is exactly the window in which
    // a second caller would read the same empty list and mint a second time.
    const minting = gate();
    const release = gate();
    const calls = stubApi({
      list: async () => jsonResponse(emptyList),
      create: async () => {
        minting.open();
        await release.reached;
        return jsonResponse(PROFILE, 201);
      },
    });

    const first = ensureProfileId();
    await minting.reached;
    const second = ensureProfileId();
    release.open();

    expect(await first).toBe("vp-1");
    expect(await second).toBe("vp-1");
    expect(countOf(calls, "POST /voice-profiles")).toBe(1);
    expect(countOf(calls, "GET /voice-profiles")).toBe(1);
  });

  it("adopts the winner's profile when its own create is refused as a conflict", async () => {
    // A caller can still lose the race to a surface outside this module (a
    // second tab, a reloaded session). The profile the winner minted is the
    // owner's one profile, so the honest answer is that id, not a conflict.
    const calls = stubApi({
      list: async (call) => jsonResponse(call === 1 ? emptyList : onePage),
      create: async () =>
        jsonResponse(
          { code: "conflict", title: "conflict", detail: "already exists" },
          409,
        ),
    });

    expect(await ensureProfileId()).toBe("vp-1");
    expect(countOf(calls, "POST /voice-profiles")).toBe(1);
    expect(countOf(calls, "GET /voice-profiles")).toBe(2);
  });

  it("raises a create refused for any other reason", async () => {
    // Only the conflict means "someone else already minted it". Recovering
    // from a 403 by re-reading would report a stale or absent profile as an
    // answer to a request the server refused outright.
    const calls = stubApi({
      list: async () => jsonResponse(emptyList),
      create: async () =>
        jsonResponse(
          {
            code: "permission_denied",
            title: "forbidden",
            detail: "voice_profile create is not granted",
          },
          403,
        ),
    });

    await expect(ensureProfileId()).rejects.toThrow(
      "voice_profile create is not granted",
    );
    expect(countOf(calls, "GET /voice-profiles")).toBe(1);
  });

  it("lets a later caller retry after a failed resolution", async () => {
    // The shared slot must be released on rejection too: a retained failure
    // would answer every later attempt for the rest of the session, and the
    // owner could never start a voice again.
    stubApi({
      list: async () => jsonResponse({ title: "server error" }, 500),
      create: async () => jsonResponse(PROFILE, 201),
    });
    await expect(ensureProfileId()).rejects.toThrow("server error");
    vi.unstubAllGlobals();

    const calls = stubApi({
      list: async () => jsonResponse(emptyList),
      create: async () => jsonResponse(PROFILE, 201),
    });
    expect(await ensureProfileId()).toBe("vp-1");
    expect(countOf(calls, "POST /voice-profiles")).toBe(1);
  });

  it("reuses a profile the owner already has instead of minting a second", async () => {
    const calls = stubApi({
      list: async () => jsonResponse(onePage),
      create: async () => {
        throw new Error("a create was issued for an owner who already has one");
      },
    });

    expect(await ensureProfileId()).toBe("vp-1");
    expect(countOf(calls, "POST /voice-profiles")).toBe(0);
  });
});
