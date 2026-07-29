import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { problemMessage } from "./common";

type VoiceProfile = components["schemas"]["VoiceProfile"];

// The caller's single Voice DNA (listVoiceProfiles caps at one). Owner-private
// and human-only server-side. It lives here rather than beside one screen
// because several surfaces report the same profile's provenance — sharing the
// query key is what makes them agree instead of each holding its own answer.
export function useVoiceProfile() {
  return useQuery({
    queryKey: ["voice-profile"],
    queryFn: async (): Promise<VoiceProfile | null> => {
      const { data, error } = await api.GET("/voice-profiles");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data[0] ?? null;
    },
  });
}

// Reuse the owner's single profile (the list caps at one) or mint it. Both
// surfaces that can collect a first writing sample — the onboarding voice step
// and the Settings card — resolve the profile through this one function: two
// independent creates would leave one owner holding two profiles, and every
// later read (the corpus, the builds, the drafts) silently picks whichever the
// list returns first.
export async function ensureProfileId(): Promise<string> {
  const list = await api.GET("/voice-profiles");
  if (list.error) {
    throw new Error(problemMessage(list.error));
  }
  const existing = list.data.data[0]?.id;
  if (existing) {
    return existing;
  }
  const created = await api.POST("/voice-profiles", {
    body: { personality_md: "" },
  });
  if (created.error) {
    throw new Error(problemMessage(created.error));
  }
  return created.data.id;
}
