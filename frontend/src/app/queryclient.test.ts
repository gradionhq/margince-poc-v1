import { afterEach, describe, expect, it, vi } from "vitest";
import { ProblemError } from "../screens/common";
import { createQueryClient, retryQuery } from "./queryclient";

// The data-layer parameters are invisible until they are wrong: a retried 4xx
// doubles a refusal the server already made final, and an unreported failure
// leaves nothing behind for whoever has to explain it. Both are pinned here.

function problem(status: number): ProblemError {
  return new ProblemError({
    type: "about:blank",
    status,
    code: "test_case",
    detail: "a server problem",
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the query retry policy", () => {
  it("retries a server error, at most twice", () => {
    expect(retryQuery(0, problem(503))).toBe(true);
    expect(retryQuery(1, problem(503))).toBe(true);
    expect(retryQuery(2, problem(503))).toBe(false);
  });

  it("never retries a client error, however early the failure", () => {
    for (const status of [400, 401, 403, 404, 409, 422, 429]) {
      expect(retryQuery(0, problem(status)), String(status)).toBe(false);
    }
  });

  it("does not retry a failure that carries no server status", () => {
    // A rejected fetch and a failure raised inside a query function: the
    // server reported neither, so FE-PARAM-2 retries neither.
    expect(retryQuery(0, new TypeError("Failed to fetch"))).toBe(false);
    expect(retryQuery(0, new Error("record not found"))).toBe(false);
  });

  it("ignores a problem body whose status is not a number", () => {
    expect(retryQuery(0, new ProblemError({ status: "503" }))).toBe(false);
    expect(retryQuery(0, new ProblemError(null))).toBe(false);
  });
});

describe("the query client defaults", () => {
  it("serves cached data for the pinned staleness window, and not on focus", () => {
    const defaults = createQueryClient().getDefaultOptions().queries;
    expect(defaults?.staleTime).toBe(30_000);
    expect(defaults?.refetchOnWindowFocus).toBe(false);
  });

  it("reports a failed query once, through the global sink", async () => {
    const reported = vi.spyOn(console, "error").mockImplementation(() => {});
    const client = createQueryClient();

    await expect(
      client.fetchQuery({
        queryKey: ["boundary-test"],
        queryFn: () => Promise.reject(new Error("the query failed")),
      }),
    ).rejects.toThrow("the query failed");

    expect(reported).toHaveBeenCalledTimes(1);
    expect(reported.mock.calls[0]?.[1]).toBeInstanceOf(Error);
  });
});
