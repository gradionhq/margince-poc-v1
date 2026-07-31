import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { initialConversationState } from "./conversation-machine";
import type { ConversationState, ThreadEntry } from "./conversation-types";
import { gateNoticeFor } from "./gate-notice";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];

// The gate has no thread, so this function is the only thing standing between a
// returning administrator and a blank screen that explains nothing. The cases
// below are the four sources of "why am I back here", plus their precedence.

const BASE_READ: CompanySiteRead = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "reading",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  pages: [],
  profile_fields: [],
  facts: [],
  comparisons: [],
  people: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "h1",
  created_at: "2026-07-31T10:00:00Z",
  updated_at: "2026-07-31T10:00:00Z",
};

function withThread(entries: readonly ThreadEntry[]): ConversationState {
  return { ...initialConversationState, thread: [...entries] };
}

const input = {
  state: initialConversationState,
  read: null,
  startError: null,
  translate: (key: string, params?: Record<string, string | number>) =>
    params === undefined ? key : `${key} ${JSON.stringify(params)}`,
  failedWithDetail: (detail: string) => `FAILED[${detail}]`,
  pausedWithDetail: (detail: string) => `PAUSED[${detail}]`,
};

describe("why the reader is back at the gate", () => {
  it("says nothing when nothing has been attempted", () => {
    expect(gateNoticeFor(input)).toBeUndefined();
  });

  it("reports a POST that never succeeded, with the server's message", () => {
    expect(
      gateNoticeFor({ ...input, startError: "site blocked automated access" }),
    ).toEqual({
      tone: "error",
      message: "FAILED[site blocked automated access]",
    });
  });

  it("reports a failed run as an error and a deferred run as paused", () => {
    expect(
      gateNoticeFor({
        ...input,
        read: { ...BASE_READ, status: "failed", status_detail: "no robots" },
      }),
    ).toEqual({ tone: "error", message: "FAILED[no robots]" });

    expect(
      gateNoticeFor({
        ...input,
        read: { ...BASE_READ, status: "deferred", status_detail: "budget" },
      }),
    ).toEqual({ tone: "paused", message: "PAUSED[budget]" });
  });

  it("treats an abandoned run as a failure, not as silence", () => {
    expect(
      gateNoticeFor({ ...input, read: { ...BASE_READ, status: "abandoned" } }),
    ).toEqual({ tone: "error", message: "FAILED[]" });
  });

  it("passes an empty detail through rather than inventing one", () => {
    // Both catalog sentences read correctly with an empty {detail}; filling the
    // gap with invented prose would be the AI claiming to know why.
    expect(
      gateNoticeFor({
        ...input,
        read: { ...BASE_READ, status: "failed", status_detail: null },
      }),
    ).toEqual({ tone: "error", message: "FAILED[]" });
  });

  it("says nothing for a run that is simply still going", () => {
    expect(
      gateNoticeFor({ ...input, read: { ...BASE_READ, status: "reading" } }),
    ).toBeUndefined();
    expect(
      gateNoticeFor({ ...input, read: { ...BASE_READ, status: "ready" } }),
    ).toBeUndefined();
  });

  it("carries a lost poll over from the thread", () => {
    const state = withThread([
      {
        kind: "narration",
        id: "7:018f-read:poll-failed",
        i18nKey: "ob.conv.read.pollFailed",
      },
    ]);
    expect(gateNoticeFor({ ...input, state })).toEqual({
      tone: "error",
      message: "ob.conv.read.pollFailed",
    });
  });

  it("carries a restore recap over, keeping a deferral paused rather than failed", () => {
    const failedRecap = withThread([
      {
        kind: "narration",
        id: "2:recap:read-failed",
        i18nKey: "ob.conv.recap.readFailed",
        params: { host: "gradion.com" },
      },
    ]);
    expect(gateNoticeFor({ ...input, state: failedRecap })).toEqual({
      tone: "error",
      message: 'ob.conv.recap.readFailed {"host":"gradion.com"}',
    });

    const deferredRecap = withThread([
      {
        kind: "narration",
        id: "2:recap:read-deferred",
        i18nKey: "ob.conv.recap.readDeferred",
        params: { host: "gradion.com" },
      },
    ]);
    expect(gateNoticeFor({ ...input, state: deferredRecap })?.tone).toBe(
      "paused",
    );
  });

  it("ignores narration that is not there to explain an absent read", () => {
    const state = withThread([
      { kind: "narration", id: "1:welcome", i18nKey: "ob.conv.welcome" },
      { kind: "narration", id: "2:started", i18nKey: "ob.conv.read.started" },
      {
        kind: "narration",
        id: "3:recap:read-reading",
        i18nKey: "ob.conv.recap.readReading",
        params: { host: "gradion.com", pages: 4 },
      },
    ]);
    expect(gateNoticeFor({ ...input, state })).toBeUndefined();
  });

  it("takes the newest explanation when the thread holds more than one", () => {
    const state = withThread([
      {
        kind: "narration",
        id: "2:recap:read-failed",
        i18nKey: "ob.conv.recap.readFailed",
        params: { host: "old.com" },
      },
      {
        kind: "narration",
        id: "9:018f-read:poll-failed",
        i18nKey: "ob.conv.read.pollFailed",
      },
    ]);
    expect(gateNoticeFor({ ...input, state })?.message).toBe(
      "ob.conv.read.pollFailed",
    );
  });

  it("lets what just happened outrank last session's recap", () => {
    const state = withThread([
      {
        kind: "narration",
        id: "2:recap:read-failed",
        i18nKey: "ob.conv.recap.readFailed",
        params: { host: "old.com" },
      },
    ]);
    expect(
      gateNoticeFor({ ...input, state, startError: "timed out" })?.message,
    ).toBe("FAILED[timed out]");
    expect(
      gateNoticeFor({
        ...input,
        state,
        read: { ...BASE_READ, status: "deferred" },
      }),
    ).toEqual({ tone: "paused", message: "PAUSED[]" });
  });
});
