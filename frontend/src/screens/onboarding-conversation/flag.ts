// The conversational onboarding ships behind an opt-in flag until it covers
// the whole journey (plan Phase 6 flips the default). Two ways to turn it on
// for a browser: a `conv` query parameter (before or inside the hash, since
// the app routes by hash) or a `margince.conv` localStorage marker.

const FLAG_STORAGE_KEY = "margince.conv";

// Web Storage can be blocked entirely (privacy modes throw on ACCESS, not
// just on write); the flag must degrade to "off" rather than crash the
// onboarding gate.
function storageFlagPresent(): boolean {
  try {
    return globalThis.localStorage.getItem(FLAG_STORAGE_KEY) !== null;
  } catch {
    return false;
  }
}

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
  return storageFlagPresent();
}
