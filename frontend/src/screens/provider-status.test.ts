// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import { en } from "../i18n/en";
import {
  canEnrichNow,
  connectionLabel,
  connectionTone,
  isRunning,
  type ProviderConnectionStatus,
  type ProviderProfileState,
  profileLabel,
  profileTone,
  roleOf,
} from "./provider-status";

// The status vocabulary two surfaces share. TypeScript proves the maps are
// TOTAL — a state added to the contract fails the build — but not that any
// entry is RIGHT: pointing `stale` at the completed message compiles clean and
// ships a page that says data is current when it is not.
//
// So these tests assert the things the type system cannot: that every state
// resolves to a real message, that the two "is a run moving" answers agree,
// and that the tones say what the states mean.

const CONNECTION_STATUSES: ProviderConnectionStatus[] = [
  "connected",
  "disconnected",
  "validating",
  "invalid_credentials",
  "insufficient_credits",
  "rate_limited",
  "provider_error",
];

const PROFILE_STATES: ProviderProfileState[] = [
  "not_connected",
  "not_eligible",
  "never_run",
  "queued",
  "in_progress",
  "completed",
  "no_match",
  "stale",
  "invalid_credentials",
  "insufficient_credits",
  "rate_limited",
  "provider_error",
  "submission_unknown",
  "completed_claims_unwritten",
];

describe("the provider status vocabulary", () => {
  it("resolves every state to a message that exists", () => {
    for (const status of CONNECTION_STATUSES) {
      expect(en[connectionLabel(status)], status).toBeTruthy();
    }
    for (const state of PROFILE_STATES) {
      expect(en[profileLabel(state)], state).toBeTruthy();
    }
  });

  it("gives every state its own sentence", () => {
    // Two states sharing one message is how "the budget ran out" comes to
    // read as "this person is not eligible" — a fact about the wallet
    // rendered as a fact about the human.
    const messages = PROFILE_STATES.map((state) => en[profileLabel(state)]);
    expect(new Set(messages).size).toBe(PROFILE_STATES.length);
  });

  it("never offers a second purchase while one is in flight", () => {
    // THE money invariant of this module. A run that is still moving cannot
    // be bought again — the live-run index refuses it — so offering the
    // button teaches a rep to click something that does nothing, and any
    // state where both answers were true would be a double-spend invitation.
    for (const state of PROFILE_STATES) {
      if (isRunning(state)) {
        expect(canEnrichNow(state), state).toBe(false);
      }
    }
  });

  it("offers the purchase exactly where one could succeed", () => {
    // not_connected has nobody to ask; not_eligible has been refused for
    // this subject. Every other terminal state is a fair retry.
    expect(canEnrichNow("not_connected")).toBe(false);
    expect(canEnrichNow("not_eligible")).toBe(false);
    expect(canEnrichNow("never_run")).toBe(true);
    expect(canEnrichNow("no_match")).toBe(true);
    expect(canEnrichNow("completed")).toBe(true);
    expect(canEnrichNow("provider_error")).toBe(true);
  });

  it("colours a refused key as danger and an unconnected provider as neither", () => {
    // A provider nobody connected is a configuration, not a fault: painting
    // it red tells an operator to fix something that is not broken. A
    // refused key IS broken and nothing will work until somebody rotates it.
    expect(connectionTone("invalid_credentials")).toBe("danger");
    expect(connectionTone("disconnected")).toBeUndefined();
    expect(connectionTone("connected")).toBe("success");
    // Recoverable vendor conditions warn rather than alarm.
    expect(connectionTone("rate_limited")).toBe("warn");
    expect(connectionTone("insufficient_credits")).toBe("warn");
  });

  it("warns on a charge with nothing to show for it", () => {
    // Paid, and the values never arrived. Neither a success nor a failure,
    // and the one state somebody has to actually see.
    expect(profileTone("completed_claims_unwritten")).toBe("warn");
    // The outcome was never learned, and the run may have been charged.
    expect(profileTone("submission_unknown")).toBe("warn");
    // Bought earlier and no longer refreshable — real data, stale label.
    expect(profileTone("stale")).toBe("warn");
    expect(profileTone("completed")).toBe("success");
  });

  it("reads the same three empty states as three different things", () => {
    const empties = ["not_connected", "not_eligible", "never_run"] as const;
    const messages = empties.map((state) => en[profileLabel(state)]);
    expect(new Set(messages).size).toBe(3);
    // None of them alarms: an empty section is not a fault.
    for (const state of empties) {
      expect(profileTone(state), state).toBeUndefined();
    }
  });
});

describe("the roster's job title", () => {
  it("prefers what a human typed over what was bought", () => {
    expect(roleOf({ title: "Head of Ops", provider_title: "VP Sales" })).toBe(
      "Head of Ops",
    );
  });

  it("fills a gap with the purchased title", () => {
    expect(roleOf({ title: null, provider_title: "VP Sales" })).toBe(
      "VP Sales",
    );
  });

  it("answers empty when there is no title at all", () => {
    // The callers render nothing on an empty string. Returning undefined
    // would work too; an empty string is what keeps `{roleOf(c) && …}` from
    // rendering a padded, wordless element.
    expect(roleOf({ title: null, provider_title: null })).toBe("");
  });
});
