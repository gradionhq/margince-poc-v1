import { QueryCache, QueryClient } from "@tanstack/react-query";
import { ProblemError } from "../screens/common";

// The data layer's parameters (architecture/frontend, FE-PARAM-1..4). The
// library's defaults are not this product's: they hold nothing back from the
// network, retry a refusal the server has already made final, and drop every
// failure on the floor. Each value below is chosen, and the ones the reader
// can feel are pinned by the tests next to this file.

// FE-PARAM-1. A query serves its cached answer for this long before a mount
// refetches in the background. A surface that needs a different window sets
// its own (the /me probe holds five minutes and refetches on focus, because a
// grant change must not sit behind a stale snapshot).
const STALE_TIME_MS = 30_000;

// FE-PARAM-2. Two retries, and only for a failure the server reported as its
// own fault.
const MAX_RETRIES = 2;

// The RFC-7807 `status` a failure carries, or null when it carries none. Only
// a ProblemError holds a server problem body, on the same terms as
// problemCodeOf: a rejected fetch, or a query function that threw a plain
// Error, never claims a status the server did not send.
function problemStatusOf(error: unknown): number | null {
  if (!(error instanceof ProblemError)) {
    return null;
  }
  const problem = error.problem;
  if (typeof problem !== "object" || problem === null || !("status" in problem))
    return null;
  return typeof problem.status === "number" ? problem.status : null;
}

// FE-PARAM-2: retry a server error, never a client error.
export function retryQuery(failureCount: number, error: Error): boolean {
  const status = problemStatusOf(error);
  // A failure that carries no status is NOT retried. It cannot be shown to be
  // the server's fault: a rejected fetch and a 4xx that a screen flattened
  // into a plain Error arrive here identically, so retrying would mean
  // retrying a client refusal, which is exactly what this parameter forbids.
  // Both already land on an error state that offers the reader a retry, so
  // nothing is lost but the silent second request.
  return status !== null && status >= 500 && failureCount < MAX_RETRIES;
}

// FE-PARAM-4: the ONE place a query failure is reported. This installation
// has no telemetry sink, so reporting means the browser console — where an
// operator can read it and the reader never sees it. It must stay that way:
// the surface whose query failed renders its own error state, and a second,
// global one would talk over it.
function reportQueryError(error: Error): void {
  console.error("margince: query failed", error);
}

// Built per call rather than exported as a module singleton so the policy can
// be exercised without importing main.tsx, which mounts the application into
// the document as a side effect of being imported.
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: STALE_TIME_MS,
        retry: retryQuery,
        // FE-PARAM-3. Returning to the tab refetches nothing by default; a
        // query whose freshness matters opts in for itself.
        refetchOnWindowFocus: false,
      },
    },
    queryCache: new QueryCache({ onError: reportQueryError }),
  });
}
