// If-Match / row-version seam. Every PATCH / merge / partner-upsert sends the
// last-seen version so a concurrent edit fails loud (409 version_skew) instead
// of silently overwriting. Mirrors the one existing usage in automations.tsx.
export function ifMatch(version?: number): { header: Record<string, string> } {
  return version === undefined
    ? { header: {} }
    : { header: { "If-Match": String(version) } };
}
