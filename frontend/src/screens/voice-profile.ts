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
