// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../../api/schema";
import type { GateNotice } from "../onboarding-gate";
import type { ConversationState, NarrationEntry } from "./conversation-types";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];

// Why the reader is looking at the gate instead of a read in progress.
//
// The gate is a single-question screen with no thread, so the narration that
// explained a previous attempt is not on it. Without this, a returning
// administrator whose earlier read failed would meet a blank gate and be told
// nothing — the message existed, it just had nowhere to appear. So the same
// facts the thread would have narrated are composed into one finished sentence.
//
// Precedence is deliberate: what just happened outranks what happened last
// session. A failed POST is the newest news of all, then this run's terminal
// state, then a lost poll, then the restore recap.

/** Narration ids that exist to explain a read the reader can no longer see. */
const EXPLANATORY_SUFFIXES = [
  ":poll-failed",
  ":recap:read-failed",
  ":recap:read-deferred",
] as const;

function explanatoryNarration(state: ConversationState): NarrationEntry | null {
  for (let index = state.thread.length - 1; index >= 0; index -= 1) {
    const entry = state.thread[index];
    if (entry === undefined || entry.kind !== "narration") {
      continue;
    }
    if (EXPLANATORY_SUFFIXES.some((suffix) => entry.id.endsWith(suffix))) {
      return entry;
    }
  }
  return null;
}

export type GateNoticeInput = Readonly<{
  state: ConversationState;
  /** The run this session started, if the server has told us about one. */
  read: CompanySiteRead | null;
  /** The POST's own failure message, when starting a read did not succeed. */
  startError: string | null;
  /**
   * Renders a catalog key. Passed in rather than imported so this stays a pure
   * function a test can drive without a provider.
   */
  translate: (
    key: NarrationEntry["i18nKey"],
    params?: Record<string, string | number>,
  ) => string;
  /** ob.gate.startFailed / ob.gate.readPaused, already translated with {detail}. */
  failedWithDetail: (detail: string) => string;
  pausedWithDetail: (detail: string) => string;
}>;

export function gateNoticeFor(input: GateNoticeInput): GateNotice | undefined {
  const { state, read, startError } = input;

  if (startError !== null) {
    return { tone: "error", message: input.failedWithDetail(startError) };
  }

  // The server's own guidance may be absent, and both sentences read correctly
  // without it — so nothing is invented to fill the gap.
  if (read?.status === "deferred") {
    return {
      tone: "paused",
      message: input.pausedWithDetail(read.status_detail ?? ""),
    };
  }
  if (read?.status === "failed" || read?.status === "abandoned") {
    return {
      tone: "error",
      message: input.failedWithDetail(read.status_detail ?? ""),
    };
  }

  const narration = explanatoryNarration(state);
  if (narration === null) {
    return undefined;
  }
  return {
    tone: narration.id.endsWith(":recap:read-deferred") ? "paused" : "error",
    message: input.translate(narration.i18nKey, narration.params),
  };
}
