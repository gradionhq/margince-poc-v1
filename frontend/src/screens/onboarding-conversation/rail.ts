// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { MessageKey } from "../../i18n/en";
import type { ConversationState } from "./conversation-types";

// Where the setup journey is, as five stops. Derived from the machine rather
// than tracked beside it, so the rail cannot disagree with the conversation.
//
// The stops are NOT the phases: READ is already finished by the time the
// two-column view first renders, and CONFIRM covers the whole clarify/review/
// manual cluster. A member never reaches voice or ready, so their rail has
// three stops — a greyed step that will never happen is a promise the flow
// does not keep.

export type RailStop = Readonly<{
  key: "read" | "confirm" | "voice" | "ready" | "connect";
  labelKey: MessageKey;
}>;

export type RailStopState = "done" | "now" | "todo";

const CREATOR_STOPS: readonly RailStop[] = [
  { key: "read", labelKey: "ob.rail.read" },
  { key: "confirm", labelKey: "ob.rail.confirm" },
  { key: "voice", labelKey: "ob.rail.voice" },
  { key: "ready", labelKey: "ob.rail.ready" },
  { key: "connect", labelKey: "ob.rail.connect" },
];

// The creator's rail minus the two stops the member path never visits.
const MEMBER_STOPS: readonly RailStop[] = CREATOR_STOPS.filter(
  (stop) => stop.key !== "voice" && stop.key !== "ready",
);

export function railStops(memberPath: boolean): readonly RailStop[] {
  return memberPath ? MEMBER_STOPS : CREATOR_STOPS;
}

// The stop the conversation is standing on. `null` while the company act is
// still reading, because the read happens on the gate rather than in the rail's
// surface and marking CONFIRM as current there would point at an empty panel.
export function currentStop(state: ConversationState): RailStop["key"] | null {
  switch (state.act) {
    case "welcome":
      return null;
    case "company":
      return state.phase === "co.intro" || state.phase === "co.reading"
        ? null
        : "confirm";
    case "voice":
      return "voice";
    case "results":
      return "ready";
    case "connect":
    case "done":
      return "connect";
  }
}

export function stopState(
  stop: RailStop["key"],
  state: ConversationState,
): RailStopState {
  const stops = railStops(state.memberPath).map((entry) => entry.key);
  const current = currentStop(state);
  const index = stops.indexOf(stop);

  // The read is its own stop and its own truth: it is done the moment the
  // server says so, whether or not the conversation has moved on.
  if (stop === "read") {
    if (state.readCompleted || currentIndexOf(current, stops) > 0) {
      return "done";
    }
    return state.phase === "co.reading" ? "now" : "todo";
  }

  const currentIndex = currentIndexOf(current, stops);
  if (currentIndex < 0 || index < 0) {
    return "todo";
  }
  if (index < currentIndex) {
    return "done";
  }
  if (index > currentIndex) {
    return "todo";
  }
  // The last stop only reads `done` once the flow actually finished, so
  // "Connect" does not claim completion while the user is still choosing.
  return stop === "connect" && state.act === "done" ? "done" : "now";
}

function currentIndexOf(
  current: RailStop["key"] | null,
  stops: readonly RailStop["key"][],
) {
  return current === null ? -1 : stops.indexOf(current);
}
