/**
 * Narrowing for `<select>` values.
 *
 * A change handler receives `event.target.value` as a plain `string`. The
 * union the state setter wants (`Region`, `Role`, `DsrKind`, …) is narrower,
 * and asserting across that gap with `as` is an unchecked claim: it holds only
 * as long as the rendered options and the union stay in step, and nothing
 * checks that they do. Browser autofill, an extension, a replayed form post,
 * or simply an option list that drifts from its type all produce a value the
 * assertion swears is impossible, and the bad value then flows into state and
 * out to the API unexamined.
 *
 * `isOption` closes it with an actual runtime check, so callers narrow by
 * evidence instead of by assertion.
 */
export function isOption<T extends string>(
  value: string,
  options: readonly T[],
): value is T {
  // The one cast here WIDENS T[] to string[] — the provably safe direction,
  // since T extends string. It exists only because Array.includes types its
  // argument as the element type, which would reject the very string we are
  // testing.
  return (options as readonly string[]).includes(value);
}
