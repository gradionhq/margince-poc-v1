// If-Match / row-version seam. Every PATCH / advance / merge / upsert sends the
// last-seen version so a concurrent edit fails loud (409 version_skew) instead
// of silently overwriting.
//
// The version is required, and that is the point: a caller holding no version
// holds no claim about what it is overwriting, and the only two honest answers
// are to refuse the write or to read the row again. Neither of them is a header
// this function can invent, so there is no shape of this call that returns an
// empty precondition.
export function ifMatch(version: number): { header: Record<string, string> } {
  return { header: { "If-Match": String(version) } };
}

// The contract states `version` as an optional field on nearly every entity, so
// a row reaches a write path typed as possibly unversioned even where the server
// always sends one. Unpinned is last-write-wins: the write lands on top of an
// edit it never saw and reports success to both editors. So the absence is a
// refusal, thrown where the write was about to happen — the mutation's own error
// path is what tells the reader the request did not go through.
export function requireVersion(version: number | undefined): number {
  if (version === undefined) {
    throw new Error("a row read back without a version takes no write");
  }
  return version;
}
