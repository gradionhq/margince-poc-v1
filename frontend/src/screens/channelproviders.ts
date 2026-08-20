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
// It resolves against BOTH halves of the directory, because a row's connector is
// not always a transport id. A record an extension unit landed on no channel
// carries the natural key's source system instead — `ext:<unit>:<system>` — and
// the same unit's channel messages carry the provider. Resolving only the first
// half is what put "Dispact" and `ext:dispact-connector:dispact` side by side in
// one list: one transport under two spellings, one of them provenance nobody
// outside this repository can parse.
//
// The fallback is still the RAW ID rather than a placeholder, and that part is
// deliberate: a provider this build has never heard of is exactly what a unit
// creates, and "telegram" read by a human beats "Unknown" or an empty cell. The
// directory is the resolver, not a gate — a label it cannot supply must not make
// the row unreadable.
export function useProviderLabel(): (provider: string) => string {
  const directory = useChannelProviders();
  const named = new Map<string, string>();
  for (const entry of directory.data?.data ?? []) {
    named.set(entry.provider, entry.label);
  }
  // Second, so a transport's own label wins any collision. The two id spaces
  // cannot overlap today — a provider id admits no `:` — and letting the
  // provider win anyway means a future widening changes nothing here.
  for (const entry of directory.data?.capture_sources ?? []) {
    if (!named.has(entry.source)) {
      named.set(entry.source, entry.label);
    }
  }
  return (provider: string) => named.get(provider) ?? provider;
}
