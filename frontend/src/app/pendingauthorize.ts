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
  const record = value as Record<string, unknown>;
  return (
    typeof record.url === "string" && typeof record.clientName === "string"
  );
}

export function stashPendingAuthorize(pending: PendingAuthorize): void {
  globalThis.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(pending));
}

// A malformed or foreign value at this key (a stray write from another
// feature, a hand-edited value, a shape from an older release) reads as
// "nothing pending" rather than throwing — the resume banner that reads
// this must never crash the app over a storage key it doesn't own alone.
export function readPendingAuthorize(): PendingAuthorize | null {
  const raw = globalThis.sessionStorage.getItem(STORAGE_KEY);
  if (!raw) {
    return null;
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    return isPendingAuthorize(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

export function clearPendingAuthorize(): void {
  globalThis.sessionStorage.removeItem(STORAGE_KEY);
}
