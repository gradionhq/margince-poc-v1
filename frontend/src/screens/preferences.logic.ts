import type { components } from "../api/schema";

export type PurposeView =
  components["schemas"]["PreferenceCenter"]["purposes"][number];

// key → subscribed. The toggle is binary; the wire state is ternary.
export type Draft = Record<string, boolean>;

// Default-deny: only an explicit grant is a subscription. "unknown" (no
// record) and "withdrawn" both mean we may not send, so both read as off.
export function displayOn(state: PurposeView["state"]): boolean {
  return state === "granted";
}

export function initialDraft(purposes: PurposeView[]): Draft {
  const draft: Draft = {};
  for (const purpose of purposes) {
    draft[purpose.key] = displayOn(purpose.state);
  }
  return draft;
}

// Locked purposes are excluded unconditionally: the server refuses them
// (preference.go:206) and the toggle is disabled, so a draft that claims one
// moved is noise, never a pending change.
export function dirtyKeys(purposes: PurposeView[], draft: Draft): string[] {
  return purposes
    .filter((purpose) => !purpose.locked)
    .filter((purpose) => draft[purpose.key] !== displayOn(purpose.state))
    .map((purpose) => purpose.key);
}

// Only changed purposes become choices. Each choice appends an immutable
// proof row, so submitting an untouched purpose would fabricate a decision
// the subject never made. `wording` carries the exact sentence rendered at
// the toggle — the wording shown IS the wording stored.
export function toChoices(
  purposes: PurposeView[],
  draft: Draft,
  wordingOf: (key: string) => string,
): Array<{
  purpose_key: string;
  state: "granted" | "withdrawn";
  wording: string;
}> {
  const changed = new Set(dirtyKeys(purposes, draft));
  return purposes
    .filter((purpose) => changed.has(purpose.key))
    .map((purpose) => ({
      purpose_key: purpose.key,
      state: draft[purpose.key] ? ("granted" as const) : ("withdrawn" as const),
      wording: wordingOf(purpose.key),
    }));
}
