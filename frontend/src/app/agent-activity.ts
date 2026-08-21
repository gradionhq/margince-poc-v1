import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { throwProblem } from "../screens/common";

type ActivityItem = components["schemas"]["ActivityItem"];

/** Fast while the agent is mid-run; slow otherwise. */
const POLL_LIVE_MS = 3_000;
const POLL_IDLE_MS = 30_000;

const ACTIVITY_KEY = ["me", "agent-activity"] as const;

/** One frozen empty list, so an absent read never mints a new array identity. */
const NOTHING: readonly ActivityItem[] = Object.freeze([]);

export type AgentActivity = Readonly<{
  /** Queued, running or awaiting-approval runs; empty while the read is absent. */
  running: readonly ActivityItem[];
  /** Runs that settled since local midnight, newest first. */
  recent: readonly ActivityItem[];
  /** Whether a run is live RIGHT NOW, as reported by a read that answered. */
  working: boolean;
}>;

/**
 * What the overnight runner is doing, polled while somebody is looking.
 *
 * The rail's doctrine applies to `working`: a read that has not answered, or
 * that this seat may not make, is ABSENT rather than a zero. So `working` is
 * false for a pending read and false for a failed one, and true only when a
 * body came back carrying a live run — nothing here lets `undefined` flicker
 * through as a standing the reader would take for "at rest".
 */
export function useAgentActivity(): AgentActivity {
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
      const { data, error } = await api.GET("/me/agent-activity");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (q) =>
      (q.state.data?.running.length ?? 0) > 0 ? POLL_LIVE_MS : POLL_IDLE_MS,
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

  const answered = query.data;
  return {
    running: answered?.running ?? NOTHING,
    recent: answered?.recent ?? NOTHING,
    working: answered !== undefined && answered.running.length > 0,
  };
}
