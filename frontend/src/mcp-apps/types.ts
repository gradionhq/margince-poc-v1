// The tool answer as it arrives from the host, and the narrowing that turns it
// into something a renderer may read.
//
// EVERY MEMBER IS OPTIONAL AND `unknown`-TYPED ON PURPOSE. What arrives in
// `structuredContent` is customer data that travelled through a host this
// document does not control, and a TypeScript interface describes it without
// checking a single byte of it at runtime. So the types below are the shape a
// renderer WANTS, and the functions beside them are the only way to get there:
// each one answers the empty case rather than throwing, because a view that
// throws mid-render leaves the human a blank panel with nothing saying why.

/** One condition the answer came with, keyed by the envelope's own code. */
export type Warning = { code: string; message?: string };

/** The envelope every tool on this surface seals its answer into. */
export type Envelope = { data?: unknown; warnings?: Warning[] };

/**
 * asWarnings keeps only the entries that carry a code, because the code is what
 * a view asks by — prose would tie the view to the server's wording.
 */
export function asWarnings(value: unknown): Warning[] {
  if (!Array.isArray(value)) return [];
  // REBUILT, not filtered through. A type predicate over the original objects
  // would hand a renderer whatever else the host attached, and would type
  // `message` as a string without ever checking that it is one — which is the
  // same "types are not validation" mistake this file exists to avoid, made by
  // the function meant to prevent it.
  return value.flatMap((entry) => {
    const warning = asRecord(entry);
    if (typeof warning.code !== "string") return [];
    return typeof warning.message === "string"
      ? [{ code: warning.code, message: warning.message }]
      : [{ code: warning.code }];
  });
}

/** asRecord answers an empty object for anything that is not one, arrays included. */
export function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    return {};
  return { ...(value as Record<string, unknown>) };
}

/** asList answers an empty list for a field the host did not send. */
export function asList(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

/** asText answers the empty string for anything that is not text. */
export function asText(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/**
 * asFiniteNumber answers null for anything a view cannot render as a number —
 * absent, NaN and Infinity alike. Null is the caller's cue to render the em
 * dash, which is the difference between "we do not have this" and "0".
 */
export function asFiniteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}
