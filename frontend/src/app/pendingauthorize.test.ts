/** @vitest-environment jsdom */
import { afterEach, describe, expect, it } from "vitest";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
} from "./pendingauthorize";

// The one place that knows how a pending OAuth authorization is spelled in
// sessionStorage (Task 7's I8 guide-with-a-return-trip; Task 8's resume
// banner is the module's second reader). The key itself is not exported —
// module isolation means a second spelling of it elsewhere is a bug — so a
// test that wants to plant a malformed value writes the same literal key the
// module owns, exactly as a foreign write from another feature would.
const STORAGE_KEY = "margince.pendingAuthorize";

// A browser that refuses session storage throws on the PROPERTY ACCESS —
// what private-mode and blocked-storage builds actually do, rather than a
// store that quietly answers null. Restored after each case so the rest of
// the suite reads the real jsdom storage.
let releaseStorage: (() => void) | null = null;

function refuseStorage(): void {
  const original = Object.getOwnPropertyDescriptor(
    globalThis,
    "sessionStorage",
  );
  releaseStorage = () => {
    Reflect.deleteProperty(globalThis, "sessionStorage");
    if (original) {
      Object.defineProperty(globalThis, "sessionStorage", original);
    }
  };
  Object.defineProperty(globalThis, "sessionStorage", {
    get() {
      throw new DOMException("storage is unavailable", "SecurityError");
    },
    configurable: true,
  });
}

afterEach(() => {
  releaseStorage?.();
  releaseStorage = null;
  clearPendingAuthorize();
});

describe("pendingauthorize", () => {
  it("round-trips a stashed authorization", () => {
    stashPendingAuthorize({
      url: "/oauth/authorize?client_id=x",
      clientName: "Claude Code",
    });
    expect(readPendingAuthorize()).toEqual({
      url: "/oauth/authorize?client_id=x",
      clientName: "Claude Code",
    });
  });

  it("reads a malformed value as absent rather than throwing", () => {
    globalThis.sessionStorage.setItem(STORAGE_KEY, "{not json");
    expect(() => readPendingAuthorize()).not.toThrow();
    expect(readPendingAuthorize()).toBeNull();
  });

  it("reads a syntactically valid but foreign-shaped value as absent", () => {
    // Valid JSON, but not this module's shape at all — a value some other
    // feature (or an older release) could plausibly have left behind.
    globalThis.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ workspaceId: "w1" }),
    );
    expect(readPendingAuthorize()).toBeNull();
  });

  it("reads a value missing one required field as absent", () => {
    globalThis.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ url: "/oauth/authorize?client_id=x" }),
    );
    expect(readPendingAuthorize()).toBeNull();
  });

  // The stash is a convenience, not a step the flow depends on: it is written
  // from a click handler that then navigates, so a storage refusal escaping
  // from here would strand the human on the screen they just left.
  it("stays silent when the browser refuses storage entirely", () => {
    refuseStorage();
    expect(() =>
      stashPendingAuthorize({
        url: "/oauth/authorize?client_id=x",
        clientName: "Claude Code",
      }),
    ).not.toThrow();
    expect(readPendingAuthorize()).toBeNull();
    expect(() => clearPendingAuthorize()).not.toThrow();
  });

  it("clears the stash so a subsequent read is absent", () => {
    stashPendingAuthorize({
      url: "/oauth/authorize?client_id=x",
      clientName: "Claude Code",
    });
    clearPendingAuthorize();
    expect(readPendingAuthorize()).toBeNull();
  });
});
