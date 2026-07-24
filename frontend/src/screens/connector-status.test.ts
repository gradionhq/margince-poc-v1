// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { errorClassKey, isUnhealthy, statusTone } from "./connector-status";

describe("statusTone", () => {
  it("gives each status its own tone so four states never collapse to two", () => {
    expect(statusTone("connected")).toBe("success");
    expect(statusTone("reauth_required")).toBe("warn");
    expect(statusTone("error")).toBe("danger");
    expect(statusTone("disconnected")).toBe(undefined);
  });
});

describe("errorClassKey", () => {
  it("maps every contract error class to its own sentence", () => {
    const keys = [
      "rate_limited",
      "unreachable",
      "auth",
      "history_gone",
      "internal",
    ].map(errorClassKey);
    expect(new Set(keys).size).toBe(5);
  });

  it("falls back for a class the server added ahead of this client", () => {
    expect(errorClassKey("quota_exhausted")).toBe("connectors.errUnknown");
  });

  it("falls back for null rather than throwing", () => {
    expect(errorClassKey(null)).toBe("connectors.errUnknown");
  });
});

describe("isUnhealthy", () => {
  it("treats only connected as healthy — home shows a line for the rest", () => {
    expect(isUnhealthy("connected")).toBe(false);
    expect(isUnhealthy("error")).toBe(true);
    expect(isUnhealthy("reauth_required")).toBe(true);
    expect(isUnhealthy("disconnected")).toBe(true);
  });
});
