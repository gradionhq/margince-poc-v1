import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { throwProblem } from "../screens/common";

/**
 * The anonymous capability probe, read by everything that needs it.
 *
 * ONE query, not one per reader. Two things now depend on it and they render at
 * different depths: the login screen offers exactly the authentication methods
 * the probe reports operational, and the release gate above it refuses to render
 * the app at all when the api's release is not the bundle's. Two `useQuery` calls
 * with the same key share a cache entry, but only if they also agree on
 * staleTime and retry — and a second inline copy that drifted on either would
 * quietly turn one shared fetch into two, one of which could hold a different
 * answer than the other.
 *
 * It is anonymous, so it answers before a session exists. That is the whole
 * reason the release gate can sit ahead of the auth boundary: a mixed-release set
 * breaks the login request itself, which is the first thing a reader would hit.
 */
export function useAuthCapabilities() {
  return useQuery({
    queryKey: ["auth-capabilities"],
    queryFn: async () => {
      const { data, error } = await api.GET("/auth/capabilities");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    staleTime: 60_000,
    retry: 1,
  });
}
