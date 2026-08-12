import { describe, expect, it } from "vitest";
import { ProblemError } from "./common";
import type { RetentionAction, RetentionScope } from "./retention.logic";
import {
  ACTION_LABEL_KEYS,
  actionLabelKey,
  effectReasonKey,
  effectTone,
  isDuplicateScope,
  parseRetainDays,
  policyEffect,
  RETENTION_ACTIONS,
  RETENTION_SCOPES,
  SCOPE_LABEL_KEYS,
  scopeLabelKey,
} from "./retention.logic";

// The three states a row can be in, the two refusals this surface tells apart,
// and the window field's guard — everything the screen decides before it
// renders anything.

describe("policyEffect", () => {
  it("an enabled policy the posture does not touch is acting", () => {
    expect(policyEffect({ enabled: true, suppressed_by_posture: false })).toBe(
      "acting",
    );
  });

  // The whole reason `suppressed_by_posture` travels on the row: enabled and
  // inert is a real state, and it must not read as either of the other two.
  it("an enabled policy the posture overrides is suppressed, not acting", () => {
    expect(policyEffect({ enabled: true, suppressed_by_posture: true })).toBe(
      "suppressed",
    );
  });

  it("a disabled policy reads as disabled, never as deleted", () => {
    expect(policyEffect({ enabled: false, suppressed_by_posture: false })).toBe(
      "disabled",
    );
  });

  // Off dominates suppressed: the server derives suppression from the action
  // alone, so a disabled destructive policy carries the flag too — and blaming
  // the posture for a rule the operator switched off would name the wrong cause.
  it("being off outranks being suppressed", () => {
    expect(policyEffect({ enabled: false, suppressed_by_posture: true })).toBe(
      "disabled",
    );
  });

  it("only the states that are not acting carry a reason and a tone", () => {
    expect(effectReasonKey("acting")).toBeNull();
    expect(effectReasonKey("suppressed")).toBe("retention.suppressedWhy");
    expect(effectReasonKey("disabled")).toBe("retention.disabledWhy");
    expect(effectTone("acting")).toBe("success");
    expect(effectTone("suppressed")).toBe("warn");
    expect(effectTone("disabled")).toBeUndefined();
  });
});

describe("the authorable vocabulary", () => {
  // Every enum member must be labellable, or the create form offers a scope
  // whose option has no words — the catalogue keys are exhaustive by type, so
  // this proves the mapping is total at runtime too.
  it("labels every scope and every action", () => {
    expect(RETENTION_SCOPES).toHaveLength(7);
    for (const scope of RETENTION_SCOPES) {
      expect(scopeLabelKey(scope)).toMatch(/^retention\.scope/);
    }
    for (const action of RETENTION_ACTIONS) {
      expect(actionLabelKey(action)).toMatch(/^retention\.action/);
    }
  });

  it("offers the least destructive action first", () => {
    expect(RETENTION_ACTIONS).toEqual(["archive", "anonymize", "erase"]);
  });
});

describe("isDuplicateScope", () => {
  function problem(code: string, status: number) {
    return new ProblemError({ title: code, status, code });
  }

  it("recognises the store's one conflict source", () => {
    expect(isDuplicateScope(problem("conflict", 409))).toBe(true);
  });

  // An unknown scope is a 422 and a delete race is a 404: neither may wear the
  // "already exists, edit that row" copy, because neither has a row to edit.
  it("does not claim a duplicate for any other refusal", () => {
    expect(isDuplicateScope(problem("validation_error", 422))).toBe(false);
    expect(isDuplicateScope(problem("not_found", 404))).toBe(false);
    expect(isDuplicateScope(new Error("network down"))).toBe(false);
    expect(isDuplicateScope(null)).toBe(false);
  });
});

describe("parseRetainDays", () => {
  it("accepts a whole number of days at or above the contract minimum", () => {
    expect(parseRetainDays("1")).toBe(1);
    expect(parseRetainDays(" 365 ")).toBe(365);
  });

  // Each of these would be a 422 the operator never needed to see.
  it("refuses anything that is not a window", () => {
    expect(parseRetainDays("")).toBeNull();
    expect(parseRetainDays("0")).toBeNull();
    expect(parseRetainDays("-30")).toBeNull();
    expect(parseRetainDays("30.5")).toBeNull();
    expect(parseRetainDays("thirty")).toBeNull();
  });
});

describe("the authorable scope list and the contract enum", () => {
  // RETENTION_SCOPES is an ordered array, which TypeScript cannot check for
  // exhaustiveness. SCOPE_LABEL_KEYS is a Record over the same generated union,
  // which it can — so the Record's keys are the authoritative set and this is
  // the gate the array's own comment points at.
  //
  // The failure this catches is silent in the worst way: a scope the server
  // accepts that the create form never offers, so the capability ships and
  // nobody can reach it.
  it("offers every scope the contract admits, exactly once", () => {
    const offered = [...RETENTION_SCOPES];
    const admitted = Object.keys(SCOPE_LABEL_KEYS) as RetentionScope[];
    expect([...offered].sort()).toEqual([...admitted].sort());
    expect(new Set(offered).size).toBe(offered.length);
  });

  it("offers every action the contract admits, exactly once", () => {
    const offered = [...RETENTION_ACTIONS];
    const admitted = Object.keys(ACTION_LABEL_KEYS) as RetentionAction[];
    expect([...offered].sort()).toEqual([...admitted].sort());
    expect(new Set(offered).size).toBe(offered.length);
  });
});
