// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  useIsFetching,
  useIsMutating,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ProblemError } from "../screens/common";

/**
 * What the APP is doing, as opposed to what the agent found.
 *
 * The bar at the bottom of the window is the one piece of chrome on every screen,
 * so it is also the only honest place for the thing every screen otherwise has to
 * draw for itself: whether the product is busy, and whether the last thing it
 * tried failed. A page that spins in its own corner tells you about that page; a
 * status light that is always in the same place tells you about the tool.
 *
 * Both halves come out of caches the app already keeps. Nothing here polls, and
 * nothing here needs a screen to remember to report anything — which is the point,
 * because the screen that forgets is the screen where the light lies.
 */

/** Long enough that a failure is legible, short enough that it is not a banner. */
const FAILED_MS = 5000;
/**
 * Reads settle constantly. Waiting before calling the tool busy keeps a cached
 * answer or a 40ms round trip from flashing the light, and holding it a moment
 * after keeps a burst of small queries from strobing it.
 */
const BUSY_ON_MS = 180;
const BUSY_OFF_MS = 320;

/**
 * Whether a failure is the TOOL's, and therefore this bar's to report.
 *
 * A 4xx is a conversation between one screen and its reader: the site read that
 * found nothing, the form that will not validate, the record somebody else
 * changed first. Those screens say so in their own words, in the place the
 * reader is looking — and a light in the corner announcing "something did not go
 * through" at the same moment is a second, vaguer telling of a story already
 * told. Worse, it is often about a DIFFERENT request than the one they just
 * clicked, which teaches them the light means nothing.
 *
 * What is left is the tool failing: a request that never got an answer, or a
 * server that answered 5xx. Nothing on the page can explain those, because the
 * page did not cause them.
 */
function isToolFailure(error: unknown): boolean {
  if (!(error instanceof ProblemError)) {
    // No RFC 7807 body at all: the request never reached a handler.
    return true;
  }
  const problem = error.problem;
  if (typeof problem !== "object" || problem === null) {
    return true;
  }
  const status = "status" in problem ? problem.status : undefined;
  if (typeof status !== "number") {
    return true;
  }
  // 501 is not a failure at all: it is how this product says a surface was never
  // wired in this deployment — the morning digest on a fresh install answers it
  // on every load, and a bar that went red for it would be red on every login.
  // What is left is the server genuinely falling over.
  return status >= 500 && status !== 501;
}

/** A boolean that has to mean it — on late, off late. */
function useSteady(live: boolean, onMs: number, offMs: number): boolean {
  const [steady, setSteady] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setSteady(live), live ? onMs : offMs);
    return () => clearTimeout(timer);
  }, [live, onMs, offMs]);
  return steady;
}

export type AppActivity = Readonly<{
  /** A read is in flight: the tool is fetching something the reader asked for. */
  reading: boolean;
  /** A write is in flight: the tool is doing something the reader asked for. */
  working: boolean;
  /** The TOOL failed just now — unreachable, or answered 5xx. */
  failed: boolean;
}>;

export function useAppActivity(): AppActivity {
  const client = useQueryClient();
  const reading = useSteady(useIsFetching() > 0, BUSY_ON_MS, BUSY_OFF_MS);
  // Writes are rarer and always deliberate, so they light the bar immediately
  // and stay lit while they run.
  const working = useIsMutating() > 0;
  const [failed, setFailed] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const lightUp = () => {
      setFailed(true);
      if (timer.current) {
        clearTimeout(timer.current);
      }
      timer.current = setTimeout(() => {
        setFailed(false);
        timer.current = null;
      }, FAILED_MS);
    };
    const onError = (event: {
      type: string;
      action?: { type: string; error?: unknown };
    }) => {
      if (event.type !== "updated" || event.action?.type !== "error") {
        return;
      }
      if (isToolFailure(event.action.error)) {
        lightUp();
      }
    };
    const stopQueries = client.getQueryCache().subscribe(onError);
    const stopMutations = client.getMutationCache().subscribe(onError);
    return () => {
      stopQueries();
      stopMutations();
      if (timer.current) {
        clearTimeout(timer.current);
      }
    };
  }, [client]);

  return { reading, working, failed };
}
