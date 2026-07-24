import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { problemMessage } from "../screens/common";

export type TierName = "auto_execute" | "confirmation_required" | "dynamic";
export type VerbTierMap = Record<string, TierName>;

// Approval `kind` → the tool verb that stages it. Any item in the approvals
// inbox is confirm-first by definition, so an unmapped kind still reads
// "confirm" — the map exists to surface the ORIGINATING tool + its catalog tier.
export const KIND_TO_VERB: Record<string, string> = {
  send_email: "send_email",
  advance_deal: "progress_deal",
  promote_lead: "promote_lead",
  coldstart: "enrich",
};

export function dotTier(tier: string | undefined): "auto" | "confirm" {
  return tier === "auto_execute" ? "auto" : "confirm";
}

export function approvalDotTier(
  kind: string,
  map: VerbTierMap,
): "auto" | "confirm" {
  const verb = KIND_TO_VERB[kind];
  return dotTier(verb ? map[verb] : undefined);
}

// verbTier reads the live catalog tier for an action verb (e.g. "progress_deal").
export function verbTier(verb: string, map: VerbTierMap): "auto" | "confirm" {
  return dotTier(map[verb]);
}

export function useAgentTierMap(): VerbTierMap {
  const query = useQuery({
    queryKey: ["agent-tools"],
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const { data, error } = await api.GET("/agent-tools");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const map: VerbTierMap = {};
  for (const tool of query.data?.data ?? []) {
    map[tool.name] = tool.tier;
  }
  return map;
}
