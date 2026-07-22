// The conversational onboarding ships behind an opt-in flag until it covers
// the whole journey (plan Phase 6 flips the default). Two ways to turn it on
// for a browser: a `conv` query parameter (before or inside the hash, since
// the app routes by hash) or a `margince.conv` localStorage marker.

const FLAG_STORAGE_KEY = "margince.conv";

export function conversationFlagEnabled(): boolean {
  const { location } = globalThis;
  if (new URLSearchParams(location.search).has("conv")) {
    return true;
  }
  const hashQueryStart = location.hash.indexOf("?");
  if (
    hashQueryStart !== -1 &&
    new URLSearchParams(location.hash.slice(hashQueryStart + 1)).has("conv")
  ) {
    return true;
  }
  return (globalThis.localStorage?.getItem(FLAG_STORAGE_KEY) ?? null) !== null;
}

// Hands this browser back to the classic stepper: every form of the flag is
// dropped, then the page reloads into the legacy onboarding route so the
// stepper mounts fresh instead of sharing state with the conversation.
export function exitToClassicOnboarding(hash = "#/onboarding"): void {
  globalThis.localStorage?.removeItem(FLAG_STORAGE_KEY);
  const url = new URL(globalThis.location.href);
  url.searchParams.delete("conv");
  url.hash = hash;
  globalThis.location.assign(url.toString());
}
