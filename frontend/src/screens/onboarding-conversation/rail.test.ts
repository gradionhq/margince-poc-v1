import { describe, expect, it } from "vitest";
import { initialConversationState } from "./conversation-machine";
import type {
  ConversationPhase,
  ConversationState,
} from "./conversation-types";
import { currentStop, railStops, stopState } from "./rail";

// The rail is derived, so these tests pin the derivation rather than a list of
// labels: given where the machine is, which stop is current and what does each
// stop read.

function at(
  act: ConversationState["act"],
  phase: ConversationPhase,
  extra: Partial<ConversationState> = {},
): ConversationState {
  return { ...initialConversationState, act, phase, ...extra };
}

describe("the setup rail's stops", () => {
  it("gives a creator five stops and a member three", () => {
    expect(railStops(false).map((stop) => stop.key)).toEqual([
      "read",
      "confirm",
      "voice",
      "ready",
      "connect",
    ]);
    expect(railStops(true).map((stop) => stop.key)).toEqual([
      "read",
      "confirm",
      "connect",
    ]);
  });

  it("never shows a member the two stops that path cannot reach", () => {
    const keys = railStops(true).map((stop) => stop.key);
    expect(keys).not.toContain("voice");
    expect(keys).not.toContain("ready");
  });
});

describe("which stop the conversation is standing on", () => {
  it("stands on no stop while the gate is still asking or reading", () => {
    expect(currentStop(at("company", "co.intro"))).toBeNull();
    expect(currentStop(at("company", "co.reading"))).toBeNull();
  });

  it("stands on confirm for every phase of the review cluster", () => {
    for (const phase of [
      "co.clarify",
      "co.review",
      "co.manual",
      "co.confirmed",
    ] as const) {
      expect(currentStop(at("company", phase)), phase).toBe("confirm");
    }
  });

  it("maps each later act to its own stop", () => {
    expect(currentStop(at("voice", "vo.invite"))).toBe("voice");
    expect(currentStop(at("results", "re.recap"))).toBe("ready");
    expect(currentStop(at("connect", "cn.consent"))).toBe("connect");
    expect(currentStop(at("done", "cn.done"))).toBe("connect");
  });

  it("stands on no stop before the flow starts", () => {
    expect(currentStop(initialConversationState)).toBeNull();
  });
});

describe("how each stop reads", () => {
  it("marks the read in progress while it runs and done when the server says so", () => {
    expect(stopState("read", at("company", "co.reading"))).toBe("now");
    expect(
      stopState("read", at("company", "co.reading", { readCompleted: true })),
    ).toBe("done");
  });

  it("keeps the read done once the conversation has moved past it, however it got there", () => {
    // The manual path never completes a read, so the stop cannot depend on
    // readCompleted alone or a hand-typed setup shows step one unfinished.
    expect(stopState("read", at("company", "co.manual"))).toBe("done");
    expect(stopState("read", at("voice", "vo.invite"))).toBe("done");
  });

  it("reads todo for the read while the gate is still asking", () => {
    expect(stopState("read", at("company", "co.intro"))).toBe("todo");
  });

  it("splits done, now and todo around the current stop", () => {
    const state = at("voice", "vo.collecting");
    expect(stopState("confirm", state)).toBe("done");
    expect(stopState("voice", state)).toBe("now");
    expect(stopState("ready", state)).toBe("todo");
    expect(stopState("connect", state)).toBe("todo");
  });

  it("holds connect at now while the user is still choosing, and only reads done when the flow finished", () => {
    expect(stopState("connect", at("connect", "cn.consent"))).toBe("now");
    expect(stopState("connect", at("done", "cn.done"))).toBe("done");
  });

  it("reads todo for a stop the current path does not contain", () => {
    const member = at("connect", "cn.consent", { memberPath: true });
    expect(stopState("voice", member)).toBe("todo");
    expect(stopState("ready", member)).toBe("todo");
    expect(stopState("connect", member)).toBe("now");
  });

  it("reads every stop as todo before the flow starts", () => {
    for (const stop of railStops(false)) {
      expect(stopState(stop.key, initialConversationState), stop.key).toBe(
        "todo",
      );
    }
  });
});
