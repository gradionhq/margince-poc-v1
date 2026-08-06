import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { throwProblem } from "./common";

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
        throwProblem(error);
      }
      return data.data[0] ?? null;
    },
  });
}

// The owner's one profile id, or null while they have none.
async function readProfileId(): Promise<string | null> {
  const { data, error, response } = await api.GET("/voice-profiles");
  // A bodiless non-2xx (a gateway 502/503/504) reaches openapi-fetch as a
  // falsy `error` with no `data`; reading "no profile yet" out of that would
  // mint a second profile for an owner who already has one.
  if (!response.ok || !data) {
    throwProblem(error);
  }
  return data.data[0]?.id ?? null;
}

// Reuse the owner's single profile (the list caps at one) or mint it.
async function resolveProfileId(): Promise<string> {
  const existing = await readProfileId();
  if (existing !== null) {
    return existing;
  }
  const created = await api.POST("/voice-profiles", {
    body: { personality_md: "" },
  });
  if (created.response.ok && created.data) {
    return created.data.id;
  }
  // A create refused as a conflict is the server's one-live-profile rule: some
  // other caller minted the owner's profile between the read above and this
  // write. That profile IS the one this call was resolving, so re-reading and
  // using the winner answers the question honestly — a lost race is not a
  // failure the user needs to see. Only the conflict recovers this way; any
  // other refusal is raised as itself.
  if (created.response.status === 409) {
    const won = await readProfileId();
    if (won !== null) {
      return won;
    }
  }
  throwProblem(created.error);
}

// The resolution in flight, shared by every caller that asks while it is open.
// The slot is released when that resolution SETTLES, whichever way it went: a
// retained rejection would answer every later attempt with a failure that is
// already over, and minting would stay broken for the rest of the session.
let profileIdInFlight: Promise<string> | null = null;

// The ONE way a Voice DNA comes into existence. Both surfaces that can collect
// a first writing sample — the onboarding voice step and the Settings card —
// resolve the profile through here: two independent creates would leave one
// owner holding two profiles, and every later read (the corpus, the builds, the
// drafts) would silently pick whichever the list returns first.
//
// One code path is necessary but not sufficient, because the two surfaces can
// travel it at the same time and both observe no profile. They are different
// components, so a guard held per component instance coalesces nothing between
// them; module scope is what makes concurrent callers one request.
export function ensureProfileId(): Promise<string> {
  profileIdInFlight ??= resolveProfileId().finally(() => {
    profileIdInFlight = null;
  });
  return profileIdInFlight;
}
