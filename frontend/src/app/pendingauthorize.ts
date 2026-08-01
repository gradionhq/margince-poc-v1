// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The one place that knows how a pending OAuth authorization is stashed
// across a trip to mint a passport (I7/I8: the empty consent screen sends
// the human away to fix "you have nothing to lend", and the request must
// survive the round trip). sessionStorage, not localStorage: a pending
// authorization belongs to this browsing session, not the device. One key,
// and every reader/writer goes through here — a second spelling of the key
// is a bug that only shows up as a resume banner (Task 8) that never
// appears.
const STORAGE_KEY = "margince.pendingAuthorize";

export type PendingAuthorize = { url: string; clientName: string };

function isPendingAuthorize(value: unknown): value is PendingAuthorize {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  return (
    "url" in value &&
    typeof value.url === "string" &&
    "clientName" in value &&
    typeof value.clientName === "string"
  );
}

// The stash is a CONVENIENCE, never a step the flow depends on: a browser that
// refuses session storage (private mode with a zero quota, third-party storage
// blocked, storage disabled outright) throws on the property access itself, and
// every one of those throws would otherwise escape from a click handler mid
// flow — stranding the human on the screen whose button they just pressed.
// Losing the stash costs them the resume banner and nothing else: the consent
// request still lives at the client they started from.
function storage(): Storage | null {
  try {
    return globalThis.sessionStorage;
  } catch {
    return null;
  }
}

export function stashPendingAuthorize(pending: PendingAuthorize): void {
  try {
    storage()?.setItem(STORAGE_KEY, JSON.stringify(pending));
  } catch {
    // A quota or security refusal — see storage(). Nothing to recover: the
    // caller is navigating away and the banner simply won't appear.
  }
}

// A malformed or foreign value at this key (a stray write from another
// feature, a hand-edited value, a shape from an older release) reads as
// "nothing pending" rather than throwing — the resume banner that reads
// this must never crash the app over a storage key it doesn't own alone.
export function readPendingAuthorize(): PendingAuthorize | null {
  try {
    const raw = storage()?.getItem(STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const parsed: unknown = JSON.parse(raw);
    return isPendingAuthorize(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

export function clearPendingAuthorize(): void {
  try {
    storage()?.removeItem(STORAGE_KEY);
  } catch {
    // Nothing readable can be left behind by a storage that refused the
    // write in the first place — see storage().
  }
}
