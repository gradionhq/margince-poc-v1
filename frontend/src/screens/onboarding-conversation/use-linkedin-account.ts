import { useMutation } from "@tanstack/react-query";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { problemMessage } from "../common";

// Saving the member's own LinkedIn answer (ADR-0078 §2.1b).
//
// The onboarding act asks for a profile URL and an authorization. Before this
// existed the question was asked and the answer thrown away, so the settings
// tab could not show a member what the CRM believed about their own account,
// and a reload lost the consent entirely.

type LinkedInAccount = components["schemas"]["LinkedInAccount"];

export function useSaveLinkedInAccount() {
  return useMutation({
    mutationFn: async (input: {
      profileUrl: string;
      connected: boolean;
    }): Promise<LinkedInAccount> => {
      const { data, error } = await api.PUT("/me/linkedin-account", {
        body: { profile_url: input.profileUrl, connected: input.connected },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}
