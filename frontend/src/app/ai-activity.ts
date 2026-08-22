// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { throwProblem } from "../screens/common";
import { displayedKinds } from "./ai-activity-lines";

type AiActivityItem = components["schemas"]["AiActivityItem"];

/** Fast while the AI is mid-occurrence; slow otherwise. */
const POLL_LIVE_MS = 3_000;
const POLL_IDLE_MS = 30_000;

const ACTIVITY_KEY = ["me", "ai-activity"] as const;

/** One frozen empty list, so an absent read never mints a new array identity. */
const NOTHING: readonly AiActivityItem[] = Object.freeze([]);

export type AiActivity = Readonly<{
  /** Occurrences that are queued, running, or live past their lease; empty while the read is absent. */
  running: readonly AiActivityItem[];
  /** Occurrences that settled since local midnight, newest first. */
  recent: readonly AiActivityItem[];
  /** Whether any AI work is live RIGHT NOW, as reported by a read that answered. */
  working: boolean;
}>;

/**
 * What the AI is doing for this person, polled while somebody is looking.
 *
 * ONE read over one projection: a scheduled run and a document reading arrive
 * through the same feed, so a new kind of AI work never adds a call here.
 *
 * The rail's doctrine applies to `working`: a read that has not answered, or
 * that this seat may not make, is ABSENT rather than a zero. So `working` is
 * false for a pending read and false for a failed one, and true only when a
 * body came back carrying a live run — nothing here lets `undefined` flicker
 * through as a standing the reader would take for "at rest".
 */
export function useAiActivity(): AiActivity {
  const client = useQueryClient();
  const [visible, setVisible] = useState(() => !document.hidden);

  useEffect(() => {
    const onVisibilityChange = () => {
      setVisible(!document.hidden);
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, []);

  const query = useQuery({
    queryKey: ACTIVITY_KEY,
    enabled: visible,
    queryFn: async () => {
      // The rail asks for the kinds it draws. Without this the server's bounds
      // fall on the complete record and can spend all ten `recent` slots on
      // work this surface renders nothing for.
      //
      // An empty list would FAIL OPEN, not closed, which is why it is refused
      // here rather than sent: openapi-fetch's query serializer drops a
      // zero-length array entirely, so the request would carry no `kinds` at
      // all and the server would answer with the complete record — the exact
      // failure this filter exists to prevent, and the server's own minItems /
      // 422 guard would never see it.
      const kinds = displayedKinds();
      if (kinds.length === 0) {
        throw new Error(
          "the rail narrates no kinds, so asking the server for them would silently ask for all of them",
        );
      }
      const { data, error } = await api.GET("/me/ai-activity", {
        params: { query: { kinds } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // Coalesced through NOTHING for the same reason the result below is: the
    // cached body is whatever the server sent, and reaching into it for a
    // length is what turns an off-contract 200 into a throw.
    refetchInterval: (q) =>
      (q.state.data?.running ?? NOTHING).length > 0
        ? POLL_LIVE_MS
        : POLL_IDLE_MS,
  });

  // The app disables focus refetching for every query (FE-PARAM-3), so
  // returning to the tab is otherwise answered out of the cache — and the
  // cached body is the one that is wrong, because the run it shows as live is
  // the run that finished while the tab was away. Hence an explicit refetch.
  //
  // It sits BELOW the query and not in the visibility listener because both
  // placements are silently inert: refetchQueries skips a query it still
  // considers disabled, and the enabled flag above is applied by the query's
  // own effect — so the ask has to be queued after that effect has run.
  const wasVisible = useRef(visible);
  useEffect(() => {
    const returned = visible && !wasVisible.current;
    wasVisible.current = visible;
    if (returned) {
      void client.refetchQueries({ queryKey: ACTIVITY_KEY });
    }
  }, [visible, client]);

  // Coalesced ONCE, and `working` is read off the coalesced list rather than
  // off the body: a 200 whose shape is not the one the contract promises is
  // another absent read, and reaching into it for a length is how the rail's
  // whole section throws instead of reporting nothing.
  const answered = query.data;
  const running = answered?.running ?? NOTHING;
  return {
    running,
    recent: answered?.recent ?? NOTHING,
    // STALLED is not working. The server derives that state for an occurrence
    // whose own source says it should have finished by now, and the chrome that
    // reads `working` pulses to say the AI is busy — so counting a stalled item
    // here would animate "still going" over a line that reads "it may have
    // stopped", softening the one verdict this state exists to deliver.
    working: running.some((item) => item.state !== "stalled"),
  };
}
