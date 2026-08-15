import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";
import { throwProblem } from "./common";

// The transport directory (ADR-0107/A158). Which messaging providers exist is a
// DEPLOYMENT fact, so the generated types carry no enum to render from — the
// server answers instead, and this is the one place the app asks.
//
// One cache key for every surface that needs a label, because the answer is
// identical for every caller and changes only on deploy: a second fetch would
// be a second copy of a value that cannot differ.
export function useChannelProviders() {
  return useQuery({
    queryKey: ["channel-providers"],
    queryFn: async () => {
      const { data, error } = await api.GET("/channel-providers");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    // It changes only when the server is redeployed, so refetching it on every
    // window focus spends a request to learn nothing.
    staleTime: Number.POSITIVE_INFINITY,
  });
}

// useProviderLabel returns a function that names a transport for a human.
//
// It falls back to the RAW PROVIDER ID rather than to a placeholder, and that
// is the deliberate part: a provider this build has never heard of is exactly
// what an extension unit creates, and "telegram" read by a human beats "Unknown"
// or an empty cell. The directory is the resolver, not a gate — a label it
// cannot supply must not make the row unreadable.
export function useProviderLabel(): (provider: string) => string {
  const directory = useChannelProviders();
  const byProvider = new Map(
    (directory.data?.data ?? []).map((entry) => [entry.provider, entry.label]),
  );
  return (provider: string) => byProvider.get(provider) ?? provider;
}
